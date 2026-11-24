package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/milan/ipagui/internal/ipa"
	"github.com/milan/ipagui/internal/store"
)

// DownloadService 是下载队列的绑定层。
//
// 并发策略：**固定 1 条**（与之对应的是 ADR-5 —— 我们与官方 CLI 共享同一个
// ipatool 状态目录，cookie jar 是文件，多路并发写会互相覆盖）。这里用一个
// 容量为 1 的信号量把下载串行化，队列本身留在账本里，重启不丢。
type DownloadService struct {
	runtime *Runtime

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	requests map[string]ipa.DownloadRequest

	// labels 记「这条任务是哪个显示版本」，只对「下载最新」有意义：
	// 那种任务没有 externalVersionID，若不留这一笔，账本里的版本列只能是空的
	// （用户最常走的恰恰是这条路，账本却答不上「我手里是哪个版本」）。
	labels map[string]string

	sem chan struct{}
}

// NewDownloadService 构造下载服务。
func NewDownloadService(runtimeInstance *Runtime) *DownloadService {
	return &DownloadService{
		runtime:  runtimeInstance,
		cancels:  map[string]context.CancelFunc{},
		requests: map[string]ipa.DownloadRequest{},
		labels:   map[string]string{},
		sem:      make(chan struct{}, 1),
	}
}

// Start 新建一条下载任务并立刻返回（真正的下载在后台跑，进度走 download:progress 事件）。
func (s *DownloadService) Start(req ipa.DownloadRequest) (store.Task, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return store.Task{}, err
	}

	platform := req.Platform
	if platform == "" {
		platform = s.runtime.Settings().Platform
	}

	req.Platform = platform

	// 先把应用信息补齐：界面上一条「没有名字的任务」是没法用的，
	// 而且补全后我们才能算出一个稳定的目标文件名。
	app, err := s.runtime.Catalog().Lookup(s.runtime.CallContext(), req.AppID, req.BundleID, platform)
	if err != nil {
		return store.Task{}, err
	}

	if req.AppID == 0 {
		req.AppID = app.ID
	}

	if req.BundleID == "" {
		req.BundleID = app.BundleID
	}

	if req.OutputPath == "" {
		directory := s.runtime.Settings().OutputDir
		if directory == "" {
			return store.Task{}, errors.New("尚未设置下载目录")
		}

		req.OutputPath = filepath.Join(
			directory,
			ipa.DownloadFileName(
				app.ID, app.Name, app.BundleID, req.ExternalVersionID, app.Version,
				// 账号段：换号后同一个 App 可能要再下一份，文件名里不带账号就会互相覆盖。
				s.runtime.Catalog().Session().AccountLabel(),
			),
		)

		// 文件名现在以 App 名称为主，不同 App 同名（两个都叫 Notes 这种）时会撞在一起；
		// 账本按 path 唯一，惄惄覆盖会把另一个 App 的记录也改写掉。
		req.OutputPath = s.avoidClobber(req.OutputPath, app.BundleID, app.ID)
	}

	if !filepath.IsAbs(req.OutputPath) {
		return store.Task{}, fmt.Errorf("下载路径必须是绝对路径：%s", req.OutputPath)
	}

	if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
		return store.Task{}, fmt.Errorf("创建下载目录失败: %w", err)
	}

	taskID := newTaskID()

	ctx, cancel := context.WithCancel(s.runtime.CallContext())

	s.mu.Lock()
	s.requests[taskID] = req
	s.cancels[taskID] = cancel
	if req.ExternalVersionID == "" {
		// 显示版本号（8.0.50 这种）只有 App Store 的查询结果知道，先记下来，
		// 下载完成时写进账本。
		s.labels[taskID] = app.Version
	}
	s.mu.Unlock()

	task, err := s.runtime.Library().SaveTask(s.runtime.CallContext(), store.Task{
		ID:         taskID,
		AppID:      req.AppID,
		BundleID:   req.BundleID,
		Name:       app.Name,
		Platform:   platform,
		VersionID:  req.ExternalVersionID,
		OutputPath: req.OutputPath,
		State:      store.TaskPending,
	})
	if err != nil {
		return store.Task{}, err
	}

	s.emitState(task, "已加入下载队列")

	go s.execute(ctx, taskID, req)

	return task, nil
}

// avoidClobber 避开「不同 App 同名 → 后下的把前一个的文件与账本记录一起覆盖」。
//
// 文件名以 App 名称为主之后这件事才变得现实（以前用 bundle id，天然唯一）：
// 账本在 path 上有唯一索引，两份不同的 App 落到同一个路径时，UpsertEntry 会直接
// 改写另一条记录 —— 用户看到的就是「刚下完 A，B 的记录变成了 A」。
//
// 只在「这个路径已经被另一个 bundle id 占着」时才改名，所以重复下载同一个 App
// 仍然覆盖它自己那一份（这是预期行为，也因此不会堆出一堆带时间戳的文件）。
func (s *DownloadService) avoidClobber(path, bundleID string, appID int64) string {
	entries, err := s.runtime.Library().ListEntries(s.runtime.CallContext(), store.EntryFilter{})
	if err != nil {
		return path
	}

	for _, entry := range entries {
		if entry.Path != path || entry.BundleID == "" || entry.BundleID == bundleID {
			continue
		}

		base := strings.TrimSuffix(path, ".ipa")

		return fmt.Sprintf("%s-%d.ipa", base, appID)
	}

	return path
}

func (s *DownloadService) execute(ctx context.Context, taskID string, req ipa.DownloadRequest) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, taskID)
		delete(s.labels, taskID)
		s.mu.Unlock()
	}()

	// 排队：容量 1 的信号量保证同一时刻只有一次下载在跑。
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.finishCanceled(taskID)

		return
	}

	s.mu.Lock()
	s.requests[taskID] = req
	s.mu.Unlock()

	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), taskID)
	if err != nil {
		return
	}

	if task.State == store.TaskCanceled {
		return
	}

	task.State = store.TaskRunning
	s.persistAndEmit(task, "开始下载")

	sink := func(progress ipa.Progress) {
		// 进度事件按 10Hz 发；账本不跟着写，避免把 SQLite 当成高频日志用。
		progress.TaskID = taskID
		s.runtime.Emit("download:progress", progress)
	}

	result, err := s.downloadWithAutoRetry(ctx, taskID, req, sink)

	task, getErr := s.runtime.Library().GetTask(s.runtime.CallContext(), taskID)
	if getErr != nil {
		task = store.Task{ID: taskID, OutputPath: req.OutputPath}
	}

	switch {
	case ctx.Err() != nil:
		task.State = store.TaskCanceled
		task.ErrorCode = "canceled"
		task.ErrorMessage = "已取消（半成品保留，可续传）"
		s.persistAndEmit(task, task.ErrorMessage)

		return

	case err != nil:
		var appError *ipa.AppError
		if errors.As(err, &appError) {
			task.ErrorCode = string(appError.Code)
			task.ErrorMessage = appError.Message
			if appError.Hint != "" {
				task.ErrorMessage += "（" + appError.Hint + "）"
			}
		} else {
			task.ErrorCode = "unknown"
			task.ErrorMessage = err.Error()
		}

		task.State = store.TaskFailed
		s.persistAndEmit(task, task.ErrorMessage)

		return
	}

	task.State = store.TaskDone
	task.ErrorCode = ""
	task.ErrorMessage = ""
	task.OutputPath = result.DestinationPath

	if info, statErr := os.Stat(result.DestinationPath); statErr == nil {
		task.BytesDone = info.Size()
		task.BytesTotal = info.Size()
	}

	s.persistAndEmit(task, "下载完成")

	// 哈希在下载完成时算一次：用户拿到成品那一刻，账本里就已经有校验值了，
	// 不用再回本地库点一次「算 sha256」。算不出来不影响下载结果 —— 账本照写，
	// 只是哈希留空（本地库那本会显示成「还没算过」）。
	digest, hashErr := store.HashFile(s.runtime.CallContext(), result.DestinationPath)
	if hashErr != nil {
		s.runtime.Emit("log:line", map[string]any{"level": "warn", "message": "下载完成，但没算出 sha256：" + hashErr.Error()})
		digest = ""
	}

	// 落账本：本地库列表就是从这里来的。
	if _, err := s.runtime.Library().UpsertEntry(s.runtime.CallContext(), store.Entry{
		AppID:        req.AppID,
		BundleID:     req.BundleID,
		Name:         task.Name,
		Version:      s.ledgerVersion(taskID, task.VersionID),
		Platform:     req.Platform,
		Path:         result.DestinationPath,
		SizeBytes:    task.BytesDone,
		SHA256:       digest,
		DownloadedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		s.runtime.Emit("download:state", map[string]any{
			"taskId":  taskID,
			"state":   string(task.State),
			"message": "下载完成，但写入本地库失败：" + err.Error(),
		})
	}

	s.runtime.Emit("library:changed", nil)

	// 「下载完成自动定位文件」以前只是设置页里一个不生效的复选框。
	if s.runtime.Settings().RevealAfterDownload {
		if revealErr := revealInFileManager(result.DestinationPath); revealErr != nil {
			s.runtime.Emit("log:line", map[string]any{"level": "warn", "message": "自动定位文件失败：" + revealErr.Error()})
		}
	}
}

const (
	// autoRetryAttempts 是在上游自己的 3 次许可/令牌重试之外，为网络抖动额外给的次数。
	autoRetryAttempts = 2
	// autoRetryBackoff 是退避基数：第 1 次等 5 秒，第 2 次等 10 秒。
	autoRetryBackoff = 5 * time.Second
)

// downloadWithAutoRetry 跑一次下载，遇到「再试一次有意义」的错误就退避后重来。
//
// 为什么值得自动重试：几百兆的下载跑到 80% 断一次 TCP，用户只能自己去下载页
// 点「续传重试」；而 .tmp 已经落盘，续传本身是秒级的事。
// 只重试网络类错误 —— 没登录、没许可、磁盘满这类重试一百次也一样。
func (s *DownloadService) downloadWithAutoRetry(ctx context.Context, taskID string, req ipa.DownloadRequest, sink ipa.ProgressSink) (ipa.DownloadResult, error) {
	result, err := s.runtime.Catalog().Download(ctx, req, sink)

	for attempt := 1; attempt <= autoRetryAttempts; attempt++ {
		if err == nil || ctx.Err() != nil || !retryableDownloadError(err) {
			return result, err
		}

		wait := autoRetryBackoff * time.Duration(attempt)

		s.announceAutoRetry(taskID, attempt, wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return result, err
		}

		result, err = s.runtime.Catalog().Download(ctx, req, sink)
	}

	return result, err
}

// announceAutoRetry 把「正在自动重试」写进任务状态，免得界面停在那儿像卡死。
func (s *DownloadService) announceAutoRetry(taskID string, attempt int, wait time.Duration) {
	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), taskID)
	if err != nil {
		return
	}

	task.State = store.TaskRunning
	task.ErrorCode = ""
	task.ErrorMessage = ""

	s.persistAndEmit(task, fmt.Sprintf("这次没成，%s 后自动重试（第 %d/%d 次，半成品保留、会接着下）", wait.Round(time.Second), attempt, autoRetryAttempts))
}

// retryableDownloadError 判断「再试一次」有没有意义。
func retryableDownloadError(err error) bool {
	if err == nil {
		return false
	}

	var appError *ipa.AppError
	if errors.As(err, &appError) {
		return appError.Code == ipa.CodeNetwork || appError.Code == ipa.CodeTemporarilyUnavailable
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return true
	}

	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// ledgerVersion 决定账本里记哪个版本：指定历史版本时记 Apple 的版本标识
// （SearchView 的「已下载」交叉引用按它匹配），下载最新时记显示版本号。
func (s *DownloadService) ledgerVersion(taskID, externalVersionID string) string {
	if externalVersionID != "" {
		return externalVersionID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.labels[taskID]
}

func (s *DownloadService) finishCanceled(taskID string) {
	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), taskID)
	if err != nil {
		return
	}

	task.State = store.TaskCanceled
	task.ErrorCode = "canceled"
	task.ErrorMessage = "已取消（排队中）"
	s.persistAndEmit(task, task.ErrorMessage)
}

func (s *DownloadService) persistAndEmit(task store.Task, message string) {
	saved, err := s.runtime.Library().SaveTask(s.runtime.CallContext(), task)
	if err != nil {
		s.runtime.Emit("log:line", map[string]any{"level": "error", "message": "写入任务状态失败: " + err.Error()})

		return
	}

	s.emitState(saved, message)
}

func (s *DownloadService) emitState(task store.Task, message string) {
	s.runtime.Emit("download:state", map[string]any{
		"taskId":       task.ID,
		"state":        string(task.State),
		"message":      message,
		"bytesDone":    task.BytesDone,
		"bytesTotal":   task.BytesTotal,
		"errorCode":    task.ErrorCode,
		"errorMessage": task.ErrorMessage,
		"outputPath":   task.OutputPath,
		"name":         task.Name,
		"platform":     task.Platform,
	})
}

// List 返回全部任务（新→旧）。
func (s *DownloadService) List() ([]store.Task, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return nil, err
	}

	return s.runtime.Library().ListTasks(s.runtime.CallContext())
}

// Cancel 取消一条排队中或正在下载的任务。半成品（.tmp 与并行分片）会保留。
func (s *DownloadService) Cancel(id string) error {
	s.mu.Lock()
	cancel, ok := s.cancels[id]
	s.mu.Unlock()

	if !ok {
		return errors.New("该任务不在运行中，无法取消")
	}

	cancel()

	return nil
}

// Retry 重新下载一条失败/取消的任务。
func (s *DownloadService) Retry(id string) (store.Task, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return store.Task{}, err
	}

	s.mu.Lock()
	req, ok := s.requests[id]
	s.mu.Unlock()

	if !ok {
		task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), id)
		if err != nil {
			return store.Task{}, err
		}

		req = ipa.DownloadRequest{
			AppID:             task.AppID,
			BundleID:          task.BundleID,
			Platform:          task.Platform,
			ExternalVersionID: task.VersionID,
			OutputPath:        task.OutputPath,
		}
	}

	ctx, cancel := context.WithCancel(s.runtime.CallContext())

	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()

	// SaveTask 是全量 upsert（ON CONFLICT 时把每一列都覆盖掉），所以这里必须带上原来的任务名：
	// 留空的话「重试」会把名字冲掉，下载页只剩 bundleId，下载完成后写进本地库的那条也是空名字。
	name := ""
	if old, err := s.runtime.Library().GetTask(s.runtime.CallContext(), id); err == nil {
		name = old.Name
	}

	task, err := s.runtime.Library().SaveTask(s.runtime.CallContext(), store.Task{
		ID:         id,
		AppID:      req.AppID,
		BundleID:   req.BundleID,
		Name:       name,
		Platform:   req.Platform,
		VersionID:  req.ExternalVersionID,
		OutputPath: req.OutputPath,
		State:      store.TaskPending,
	})
	if err != nil {
		return store.Task{}, err
	}

	s.emitState(task, "已重新加入队列")

	go s.execute(ctx, id, req)

	return task, nil
}

// Remove 从队列里移除任务记录（不动磁盘文件）。
func (s *DownloadService) Remove(id string) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.requests, id)
	s.mu.Unlock()

	return s.runtime.Library().DeleteTask(s.runtime.CallContext(), id)
}

// Reveal 在文件管理器里定位任务对应的文件。
func (s *DownloadService) Reveal(id string) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), id)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(task.OutputPath); statErr != nil {
		return fmt.Errorf("文件尚不存在：%s", task.OutputPath)
	}

	return revealInFileManager(task.OutputPath)
}

// FreeSpace 返回下载目录所在磁盘的可用空间。
//
// 界面上是「批量下载前先算一笔总账」用的：宁可提前提醒，也别下到一半报磁盘满。
func (s *DownloadService) FreeSpace() (int64, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return 0, err
	}

	directory := s.runtime.Settings().OutputDir
	if directory == "" {
		return 0, errors.New("尚未设置下载目录")
	}

	return ipa.FreeSpace(directory)
}

// ResetPartial 删除半成品（.tmp 与并行下载的 .ipatool-parts 目录），让下一次下载从头开始。
func (s *DownloadService) ResetPartial(id string) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), id)
	if err != nil {
		return err
	}

	return ipa.ResetPartial(task.OutputPath)
}

// PartialSize 返回这个任务已下载的分片大小（用于界面显示「已缓存 123 MB」）。
func (s *DownloadService) PartialSize(id string) (int64, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return 0, err
	}

	task, err := s.runtime.Library().GetTask(s.runtime.CallContext(), id)
	if err != nil {
		return 0, err
	}

	size, _ := ipa.PartialSize(task.OutputPath + ".tmp")

	return size, nil
}

// newTaskID 生成带时间前缀的任务 id（便于按加入顺序阅读，且天然唯一）。
func newTaskID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(buffer))
}
