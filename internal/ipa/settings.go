package ipa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Settings 是应用的持久化设置。
//
// 刻意**不**包含任何秘密：文件后端所需的「口令」存在操作系统的凭据库里
// （见 secret.go），而不是明文落在这个 JSON 里。
type Settings struct {
	// OutputDir 是默认下载目录（不是文件路径，是目录）。
	OutputDir string `json:"outputDir"`
	// Platform 是默认平台，取值见 PlatformChoices。
	Platform string `json:"platform"`
	// AcquireLicense 对应下载时的 --purchase：缺许可时自动获取（仅免费应用）。
	AcquireLicense bool `json:"acquireLicense"`
	// RevealAfterDownload 完成后是否在文件管理器里高亮。
	RevealAfterDownload bool `json:"revealAfterDownload"`
}

// DefaultSettings 给出首次运行时的默认值。
func DefaultSettings() Settings {
	return Settings{
		OutputDir:           defaultOutputDir(),
		Platform:            "iphone",
		AcquireLicense:      false,
		RevealAfterDownload: false,
	}
}

func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Windows 的「下载」目录可能被本地化（例如「下载」），这里不做猜测：
	// 用 HOME/Downloads 作为默认，用户在设置里改一次即可。
	return filepath.Join(home, "Downloads", "IPA")
}

// Validate 校验设置，并顺手把缺失项补上默认值。
//
// 允许 OutputDir 为空（表示尚未决定），调用方在真正下载前必须解析出绝对路径。
func (s *Settings) Validate() error {
	if s.Platform == "" {
		s.Platform = "iphone"
	}

	if _, err := ParsePlatform(s.Platform); err != nil {
		return fmt.Errorf("不支持的平台 %q", s.Platform)
	}

	if s.OutputDir != "" && !filepath.IsAbs(s.OutputDir) {
		return fmt.Errorf("输出目录必须是绝对路径：%s", s.OutputDir)
	}

	return nil
}

// ConfigDir 返回应用配置目录（与 ipatool 的状态目录彼此独立）。
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}

	return filepath.Join(base, "ipagui"), nil
}

func settingsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "settings.json"), nil
}

// LoadSettings 读设置。文件不存在时返回默认值（而不是报错）。
func LoadSettings() (Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return Settings{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSettings(), nil
		}

		return Settings{}, fmt.Errorf("读取设置失败: %w", err)
	}

	settings := DefaultSettings()
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("解析设置失败（%s）: %w", path, err)
	}

	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}

	return settings, nil
}

// SaveSettings 原子写入设置（先写临时文件再 rename），避免崩溃时留下半个文件。
func SaveSettings(settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}

	path, err := settingsPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化设置失败: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入设置失败: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("保存设置失败: %w", err)
	}

	return nil
}
