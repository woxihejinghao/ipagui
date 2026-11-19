package services

import (
	"errors"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/milan/ipagui/internal/ipa"
)

// SettingsService 是设置页的绑定层。
type SettingsService struct {
	runtime *Runtime
}

// NewSettingsService 构建设置服务。
func NewSettingsService(runtimeInstance *Runtime) *SettingsService {
	return &SettingsService{runtime: runtimeInstance}
}

// Get 返回当前设置。
func (s *SettingsService) Get() ipa.Settings {
	return s.runtime.Settings()
}

// Save 保存设置。
func (s *SettingsService) Save(settings ipa.Settings) error {
	return s.runtime.SaveSettings(settings)
}

// ChooseOutputDir 打开原生目录选择框，返回用户选中的绝对路径。
//
// 返回空字符串表示用户取消了选择（不是错误）。
func (s *SettingsService) ChooseOutputDir() (string, error) {
	ctx := s.runtime.Context()
	if ctx == nil {
		return "", errors.New("界面尚未就绪")
	}

	selected, err := wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:                "选择 IPA 下载目录",
		DefaultDirectory:     s.runtime.Settings().OutputDir,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}

	return selected, nil
}

// PassphraseStatus 描述文件后端口令的现状（Windows 上会用到）。
type PassphraseStatus struct {
	// SystemStoreAvailable 表示操作系统的凭据库可用（能安全保存口令）。
	SystemStoreAvailable bool `json:"systemStoreAvailable"`
	// HasPassphrase 表示已经保存过口令。
	HasPassphrase bool `json:"hasPassphrase"`
	// ExpectedBackend 是本平台「应该」拿到的后端。
	ExpectedBackend string `json:"expectedBackend"`
	// PickedBackend 是实际拿到的后端（可能为空）。
	PickedBackend string `json:"pickedBackend"`
	// MatchesExpected 是两者是否一致；不一致说明构建方式出了问题（例如 macOS 没开 cgo）。
	MatchesExpected bool   `json:"matchesExpected"`
	Hint            string `json:"hint"`
}

// PassphraseStatus 报告凭据后端与口令状态。
func (s *SettingsService) PassphraseStatus() PassphraseStatus {
	store := ipa.NewAppSecretStore()

	status := PassphraseStatus{
		SystemStoreAvailable: store.Available(),
		ExpectedBackend:      string(ipa.ExpectedBackend()),
	}

	if _, ok := store.Load(); ok {
		status.HasPassphrase = true
	}

	if picked, ok := ipa.PickedBackend(); ok {
		status.PickedBackend = string(picked)
	}

	status.MatchesExpected = status.PickedBackend != "" && status.PickedBackend == status.ExpectedBackend

	switch {
	case status.PickedBackend == "":
		status.Hint = "尚未确定凭据后端：请先完成一次登录或查看环境自检结果"
	case status.PickedBackend == "keychain":
		status.Hint = "凭据存在系统钥匙串中，与官方 ipatool CLI 共用"
	case status.PickedBackend == "wincred":
		status.Hint = "凭据存在 Windows 凭据管理器中"
	case status.PickedBackend == "file":
		if status.HasPassphrase {
			status.Hint = "凭据以文件形式加密保存在 ipatool 状态目录；口令已保存"
		} else if status.SystemStoreAvailable {
			status.Hint = "凭据以文件形式加密保存，需要口令；请在此设置一次口令"
		} else {
			status.Hint = "凭据以文件形式加密保存，需要口令；系统凭据库不可用，请用环境变量 IPAGUI_KEYCHAIN_PASSPHRASE 或每次启动时输入"
		}
	default:
		status.Hint = "凭据后端：" + status.PickedBackend
	}

	return status
}

// SetPassphrase 把文件后端口令存进系统凭据库（不落明文到磁盘）。
func (s *SettingsService) SetPassphrase(value string) error {
	return ipa.NewAppSecretStore().Save(value)
}

// ClearPassphrase 删除已保存的口令。
func (s *SettingsService) ClearPassphrase() error {
	return ipa.NewAppSecretStore().Delete()
}
