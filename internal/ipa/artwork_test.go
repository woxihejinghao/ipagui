package ipa

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jpegMagic 是一段最小的 JPEG 头，够 http.DetectContentType 认出 image/jpeg。
var jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

// 这条测试守的是「商铺头真的发出去了」：不带 X-Apple-Store-Front 时 Apple 按美国商铺
// 回答（实测 trackViewUrl 变 /us/），中国区独有的 App 会直接查不到 —— 界面上表现为
// 「所有 App 都没图标」，而这是最难排查的一类坏法，所以在服务端断言请求本身。
func TestStoreMetadataLookupSendsStoreFront(t *testing.T) {
	const storeFront = "143524-2,34"

	var (
		gotPath   string
		gotID     string
		gotHeader string
	)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotID = request.URL.Query().Get("id")
		gotHeader = request.Header.Get("X-Apple-Store-Front")

		// 体积用**带引号的字符串**：这就是实测的真实形态（2026-10 /lookup 返回
		// "967153664"）。写成数字时这条测试会漏掉「decode 失败 → 图标全没」那个坏法。
		_, _ = writer.Write([]byte(`{"resultCount":3,"results":[
			{"trackId":414478124,"artworkUrl512":"https://is1-ssl.mzstatic.com/wechat/512x512bb.jpg","currentVersionReleaseDate":"2026-09-29T01:36:22Z","fileSizeBytes":"967153664"},
			{"trackId":123,"artworkUrl512":"","fileSizeBytes":0},
			{"trackId":999,"fileSizeBytes":{"unexpected":"object"}}
		]}`))
	}))
	defer server.Close()

	client := &storeMetadataClient{baseURL: server.URL, client: server.Client()}

	metadata, err := client.lookup(context.Background(), storeFront, []int64{414478124, 123, 999})
	if err != nil {
		t.Fatalf("lookup 失败: %v", err)
	}

	if gotPath != "/lookup" {
		t.Errorf("请求路径 = %q, 期望 /lookup", gotPath)
	}

	if gotID != "414478124,123,999" {
		t.Errorf("id 参数 = %q, 期望 414478124,123,999", gotID)
	}

	if gotHeader != storeFront {
		t.Errorf("X-Apple-Store-Front = %q, 期望 %q（丢了它就会拿到美国商铺的结果）", gotHeader, storeFront)
	}

	if metadata[414478124].ArtworkURL != "https://is1-ssl.mzstatic.com/wechat/512x512bb.jpg" {
		t.Errorf("图标 URL = %q", metadata[414478124].ArtworkURL)
	}

	if metadata[414478124].FileSizeBytes != 967153664 {
		t.Errorf("体积 = %d, 期望 967153664（字符串形态也要能解）", metadata[414478124].FileSizeBytes)
	}

	if metadata[414478124].CurrentVersionReleaseDate != "2026-09-29T01:36:22Z" {
		t.Errorf("更新时间 = %q, 期望 2026-09-29T01:36:22Z", metadata[414478124].CurrentVersionReleaseDate)
	}

	if _, ok := metadata[123]; !ok {
		t.Errorf("取不到图标的 App 也应该在结果里（界面按缺省值渲染），实际没有")
	}

	if _, ok := metadata[999]; ok {
		t.Errorf("体积是坏类型的条目不该混进结果")
	}

	if metadata[123].ArtworkURL != "" || metadata[414478124].ArtworkURL == "" {
		t.Errorf("坏条目不应影响好条目：123 的图标 = %q, 414478124 的图标 = %q",
			metadata[123].ArtworkURL, metadata[414478124].ArtworkURL)
	}
}

func TestStoreMetadataLookupRejectsNonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := &storeMetadataClient{baseURL: server.URL, client: server.Client()}

	if _, err := client.lookup(context.Background(), "143465-2,29", []int64{1}); err == nil {
		t.Fatal("HTTP 403 时期望返回错误，实际 nil")
	}
}

// 富化失败必须是「少两列」，不能是「搜索失败」：图标与体积是装饰。
func TestEnrichAppViewsKeepsViewsWhenLookupFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	services := &Services{metadata: &storeMetadataClient{baseURL: server.URL, client: server.Client()}}
	views := []AppView{{ID: 1, Name: "微信"}}

	got := services.enrichAppViews(context.Background(), "143465-2,29", views)
	if len(got) != 1 || got[0].Name != "微信" {
		t.Fatalf("富化失败后视图被破坏: %+v", got)
	}

	if got[0].ArtworkURL != "" || got[0].FileSizeBytes != 0 || got[0].UpdatedAt != "" {
		t.Fatalf("富化失败时不该写入任何值: %+v", got[0])
	}
}

func TestEnrichAppViewsMergesByTrackID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"resultCount":1,"results":[
			{"trackId":2,"artworkUrl512":"https://example.test/icon.jpg","currentVersionReleaseDate":"2026-09-29T01:36:22Z","fileSizeBytes":1024}
		]}`))
	}))
	defer server.Close()

	services := &Services{metadata: &storeMetadataClient{baseURL: server.URL, client: server.Client()}}
	views := []AppView{{ID: 1, Name: "甲"}, {ID: 2, Name: "乙"}}

	got := services.enrichAppViews(context.Background(), "143465-2,29", views)

	if got[0].ArtworkURL != "" || got[0].FileSizeBytes != 0 || got[0].UpdatedAt != "" {
		t.Errorf("只返回了 id=2，id=1 不该被写上值: %+v", got[0])
	}

	if got[1].ArtworkURL != "https://example.test/icon.jpg" || got[1].FileSizeBytes != 1024 {
		t.Errorf("id=2 没拿到元数据: %+v", got[1])
	}

	// 更新时间也要过到视图上：界面上它就跟在版本号后面（「更新 2026/9/29」）。
	if got[1].UpdatedAt != "2026-09-29T01:36:22Z" {
		t.Errorf("id=2 的更新时间 = %q, 期望 2026-09-29T01:36:22Z", got[1].UpdatedAt)
	}
}

// ReadIPArtwork 守的是「本地库图标来自包里的 iTunesArtwork 且就是那张图」。
func TestReadIPArtwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wechat_857109923.ipa")

	if err := writeTestIPA(path, true); err != nil {
		t.Fatalf("造测试包失败: %v", err)
	}

	data, err := ReadIPArtwork(path)
	if err != nil {
		t.Fatalf("ReadIPArtwork 失败: %v", err)
	}

	if string(data) != string(jpegMagic) {
		t.Errorf("图标字节 = %v, 期望 %v", data, jpegMagic)
	}

	withoutArtwork := filepath.Join(t.TempDir(), "no-artwork.ipa")
	if err := writeTestIPA(withoutArtwork, false); err != nil {
		t.Fatalf("造测试包失败: %v", err)
	}

	if _, err := ReadIPArtwork(withoutArtwork); err == nil || !strings.Contains(err.Error(), iTunesArtworkName) {
		t.Errorf("没有 iTunesArtwork 时期望点名它的错误，实际: %v", err)
	}

	notZip := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notZip, []byte("我只是个文本"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	if _, err := ReadIPArtwork(notZip); err == nil {
		t.Error("非 zip 文件期望报错，实际 nil")
	}
}

func TestArtworkDataURI(t *testing.T) {
	uri := ArtworkDataURI(jpegMagic)

	if !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("data URI 前缀不对: %q", uri)
	}

	if strings.ContainsAny(uri, " \n") {
		t.Errorf("data URI 里不该有空白: %q", uri)
	}
}

func writeTestIPA(path string, withArtwork bool) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := zip.NewWriter(file)

	payload, err := writer.Create("Payload/WeChat.app/Info.plist")
	if err != nil {
		return err
	}

	if _, err := payload.Write([]byte("<plist/>")); err != nil {
		return err
	}

	if withArtwork {
		artwork, err := writer.Create(iTunesArtworkName)
		if err != nil {
			return err
		}

		if _, err := artwork.Write(jpegMagic); err != nil {
			return err
		}
	}

	return writer.Close()
}
