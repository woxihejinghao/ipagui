package ipa

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// iTunesAPIBaseURL 是 Apple 的公开目录接口（搜索与 lookup 都在这个域名下）。
//
// 上游把它写成常量（pkg/appstore/constants.go 的 iTunesAPIDomain），这里独立一份：
// 下面的调用不经过上游，而是我们直接问 Apple —— 因为上游的解码结构体把图标与体积
// 丢掉了（见 storeMetadata 的注释）。
const iTunesAPIBaseURL = "https://itunes.apple.com"

// iTunesArtworkName 是 IPA 里图标的条目名（没有扩展名，内容是 JPEG 或 PNG）。
const iTunesArtworkName = "iTunesArtwork"

// maxArtworkBytes 是图标读取上限：正常图标 20KB–300KB，8MB 只用来挡住异常包。
const maxArtworkBytes = 8 << 20

// storeMetadata 是我们要从公开接口额外补的两项：图标与体积。
//
// 为什么不在上游拿：上游的 appstore.App 只声明了 7 个字段
// （trackId / bundleId / trackName / version / price / purchaseDate / platforms），
// JSON 解码时其余 30 多项 —— 包括 artworkUrl512 与 fileSizeBytes —— 会被直接丢掉，
// 而且那是上游的内部结构体，我们改不了。所以这里按同一批 app id 再问一次。
type storeMetadata struct {
	ArtworkURL string `json:"artworkUrl512"`
	// CurrentVersionReleaseDate 是**当前商店版本**的发布时间（RFC3339 字符串，
	// 实测形如 "2026-09-29T01:36:22Z"），界面显示为「更新 …」。
	CurrentVersionReleaseDate string    `json:"currentVersionReleaseDate"`
	FileSizeBytes             byteCount `json:"fileSizeBytes"`
}

// byteCount 解码 Apple 的体积字段：同一个接口有时给数字，有时给**带引号的字符串**
// （2026-10 实测 /lookup 返回的就是 "967153664"）。
//
// 为什么值得单开一个类型：把它写成 int64 时整个响应会解码失败，而富化是「失败就什么都
// 不补」—— 于是图标也一起没了。这不是推演出来的：真实跑了一次数搜索，界面上零张图，
// 而接口明明返回了 artworkUrl512（见 §8 表 #13）。
type byteCount int64

func (b *byteCount) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if text == "" || text == "null" {
		*b = 0
		return nil
	}

	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		*b = byteCount(value)
		return nil
	}

	// 也有带小数的形态（"1234.0"）：体积不需要小数精度，取整数部分。
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("不是有效的体积值 %q: %w", text, err)
	}

	*b = byteCount(int64(value))

	return nil
}

// storeMetadataClient 只做一件事：用账号的商铺去公开接口补图标与体积。
type storeMetadataClient struct {
	baseURL string
	client  *http.Client
}

func newStoreMetadataClient() *storeMetadataClient {
	return &storeMetadataClient{
		baseURL: iTunesAPIBaseURL,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// lookup 按 app id 批量取元数据。
//
// storeFront 直接进 X-Apple-Store-Front 头（形如 143524-2,34）：实测不带这个头时
// Apple 按美国商铺回答（trackViewUrl 变成 /us/），某个商铺独有的 App 在别的商铺里
// 根本查不到 —— 图标只是装饰，但「装饰全丢」和「商铺不对」是两件事。
func (c *storeMetadataClient) lookup(ctx context.Context, storeFront string, appIDs []int64) (map[int64]storeMetadata, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(appIDs))
	for _, appID := range appIDs {
		ids = append(ids, strconv.FormatInt(appID, 10))
	}

	target := c.baseURL + "/lookup?" + url.Values{"id": {strings.Join(ids, ",")}}.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build lookup request: %w", err)
	}

	if storeFront != "" {
		request.Header.Set("X-Apple-Store-Front", storeFront)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("lookup request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lookup returned HTTP %d", response.StatusCode)
	}

	var payload struct {
		Results []json.RawMessage `json:"results"`
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode lookup response: %w", err)
	}

	// 逐个条目解码，而不是一次解成结构体切片：Apple 的字段类型偶尔会变（体积就出现过
	// 字符串），一次解全部 —— 一条怪数据就把整批图标带走。逐条解，坏的只丢它自己。
	metadata := make(map[int64]storeMetadata, len(payload.Results))

	for _, raw := range payload.Results {
		var item struct {
			TrackID int64 `json:"trackId"`
			storeMetadata
		}

		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}

		metadata[item.TrackID] = item.storeMetadata
	}

	return metadata, nil
}

// enrichAppViews 给界面视图补图标与体积。
//
// 故意不返回错误：这两项是装饰，Apple 抽风或商铺对不上时界面照旧可用，只是没有
// 图标、体积显示为「—」，而不是整个搜索失败。
func (s *Services) enrichAppViews(ctx context.Context, storeFront string, views []AppView) []AppView {
	if len(views) == 0 {
		return views
	}

	appIDs := make([]int64, 0, len(views))
	for _, view := range views {
		appIDs = append(appIDs, view.ID)
	}

	metadata, err := s.metadata.lookup(ctx, storeFront, appIDs)
	if err != nil {
		return views
	}

	for index, view := range views {
		item, ok := metadata[view.ID]
		if !ok {
			continue
		}

		views[index].ArtworkURL = item.ArtworkURL
		views[index].FileSizeBytes = int64(item.FileSizeBytes)
		views[index].UpdatedAt = item.CurrentVersionReleaseDate
	}

	return views
}

// enrichAppView 是单个视图的版本，省得调用方到处写切片。
func (s *Services) enrichAppView(ctx context.Context, storeFront string, view AppView) AppView {
	return s.enrichAppViews(ctx, storeFront, []AppView{view})[0]
}

// ReadIPArtwork 从已下载的 IPA 里取出 iTunesArtwork。
//
// 为什么用包里的而不是再问一次 Apple：公开接口只给「当前商店版本」的图标，而本地库
// 里的每一份是**某个历史版本**。上游下载时已经把那个版本的图标写了进去
// （pkg/appstore/appstore_download.go 的 applyPatches → iTunesArtwork），
// 离线可读，而且图标与包里的版本严格一致 —— 这正是版本考古在意的。
func ReadIPArtwork(path string) ([]byte, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open IPA: %w", err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if file.Name != iTunesArtworkName {
			continue
		}

		if file.UncompressedSize64 > maxArtworkBytes {
			return nil, fmt.Errorf("%s is unexpectedly large (%d bytes)", iTunesArtworkName, file.UncompressedSize64)
		}

		reader, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", iTunesArtworkName, err)
		}
		defer reader.Close()

		data, err := io.ReadAll(io.LimitReader(reader, maxArtworkBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", iTunesArtworkName, err)
		}

		if len(data) == 0 {
			return nil, errors.New(iTunesArtworkName + " is empty")
		}

		return data, nil
	}

	return nil, fmt.Errorf("no %s in this IPA", iTunesArtworkName)
}

// ArtworkDataURI 把图标字节包成 data: URI，前端可以直接塞进 img.src，
// 不必为了这张图再起一个静态文件服务或写临时文件。
func ArtworkDataURI(data []byte) string {
	return "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
}
