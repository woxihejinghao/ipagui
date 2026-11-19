package ipa

import (
	"context"
	"io"
	"math"
	"time"

	"github.com/schollz/progressbar/v3"
)

// ProgressInterval 是 GUI 进度推送频率。10Hz 对进度条足够，也不会把 IPC 打满。
const ProgressInterval = 100 * time.Millisecond

// Progress 是推给界面的结构化进度快照。
//
// 为什么不解析文本：CLI 模式下进度条是渲染到 stdout 的字符动画，
// 解析它既脆弱又拿不到字节数。走库集成后我们持有上游写进度用的
// *progressbar.ProgressBar 指针，可以直接读它的内部状态。
type Progress struct {
	TaskID        string  `json:"taskId"`
	Phase         string  `json:"phase"`
	Current       int64   `json:"current"`
	Total         int64   `json:"total"`
	Percent       float64 `json:"percent"`
	BytesPerSec   float64 `json:"bytesPerSec"`
	ETASeconds    float64 `json:"etaSeconds"`
	Indeterminate bool    `json:"indeterminate"`
}

// ProgressSink 接收进度快照。实现方必须是「非阻塞」的（例如只做事件发送）。
type ProgressSink func(Progress)

// ProgressBar 暴露给调用方的类型别名，避免调用方 import progressbar。
type ProgressBar = progressbar.ProgressBar

// NewProgressBar 创建一个**不输出任何字符**的进度条。
//
// 关键：必须把 writer 设为 io.Discard。默认 writer 是 os.Stdout，
// 而在 GUI 进程里 stdout 可能被日志或协议占用，字符动画会污染它。
//
// 初始 max = 1 是上游的用法（cmd/download.go 也是 NewOptions64(1, ...)），
// 上游拿到 HTTP 响应头之后才调用 ChangeMax64(total)。
// 因此 GetMax64() <= 1 就是「分母未知」的判据。
func NewProgressBar() *progressbar.ProgressBar {
	return progressbar.NewOptions64(1,
		progressbar.OptionSetWriter(io.Discard),
		progressbar.OptionShowBytes(true),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionSetRenderBlankState(true),
	)
}

// Snapshot 读一次进度条状态，转成结构化快照。
//
// 并发说明（progressbar v3.13.1 实测）：
//   - State() 内部加锁，返回一致快照 → 可以安全地从任意 goroutine 轮询；
//   - GetMax64() 直接读 p.config.max，**不加锁** → 只能当作提示值，
//     不能拿它做「进度绝不会回退」之类的强断言。
func Snapshot(taskID, phase string, bar *progressbar.ProgressBar) Progress {
	state := bar.State() // 加锁快照
	result := Progress{
		TaskID:      taskID,
		Phase:       phase,
		Current:     int64(state.CurrentBytes),
		BytesPerSec: sanitize(state.KBsPerSecond * 1024),
		ETASeconds:  sanitize(state.SecondsLeft),
	}

	max := bar.GetMax64()
	if max <= 1 {
		// 上游还没拿到 Content-Length（或响应无长度）：进度不可知，
		// 界面应显示不确定态动画，而不是把 0% 当成真实进度。
		result.Indeterminate = true

		return result
	}

	result.Total = max
	result.Percent = sanitizePercent(state.CurrentPercent)

	return result
}

// PollProgress 以固定频率把进度推给 sink，直到 ctx 结束。
//
// 上游的 Download 是同步阻塞调用，没有进度回调，所以只能轮询。
func PollProgress(ctx context.Context, taskID, phase string, bar *progressbar.ProgressBar, sink ProgressSink) {
	if sink == nil {
		return
	}

	ticker := time.NewTicker(ProgressInterval)
	defer ticker.Stop()

	var last Progress

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := Snapshot(taskID, phase, bar)
			if current == last {
				continue // Progress 全是可比较字段，无变化就不打扰界面
			}

			last = current
			sink(current)
		}
	}
}

// PollProgressUntil 在下载 goroutine 之外启动轮询，返回一个「停止并等待」函数。
// 调用方必须调用 stop（否则会泄漏一个 goroutine）。
func PollProgressUntil(taskID, phase string, bar *progressbar.ProgressBar, sink ProgressSink) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		PollProgress(ctx, taskID, phase, bar, sink)
	}()

	return func() {
		cancel()
		<-done
	}
}

func sanitize(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}

	return value
}

func sanitizePercent(value float64) float64 {
	value = sanitize(value)

	if value > 1 {
		value = 1
	}

	// 保留 4 位小数（0.01% 精度），避免把 long double 噪声推给界面。
	return math.Round(value*10000) / 10000
}
