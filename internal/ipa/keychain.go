package ipa

import (
	"errors"
	"fmt"
	"os"

	"github.com/byteness/keyring"
	"github.com/majd/ipatool/v2/pkg/keychain"
)

// ErrPassphraseRequired 在「需要文件后端口令但没人能提供口令」时返回。
var ErrPassphraseRequired = errors.New(
	"keychain passphrase is required: the OS credential store is unavailable, " +
		"so ipatool falls back to its encrypted file backend")

// PassphraseSource 为文件后端提供口令。
//
// 为什么需要它：上游 white list 是 [KeychainBackend, SecretServiceBackend, FileBackend]，
// 而 byteness/keyring 只在 darwin(cgo) 注册 keychain 后端、在 windows 注册的是
// **WinCredBackend（上游白名单里没有！）**。所以在 Windows 上必然落到 FileBackend，
// 需要一个口令；macOS 上走系统钥匙串，不需要口令。
type PassphraseSource interface {
	// Passphrase 返回口令；返回空串表示「没有可用口令」。
	Passphrase() (string, error)
}

type passphraseFunc func() (string, error)

func (f passphraseFunc) Passphrase() (string, error) { return f() }

// StaticPassphrase 返回固定口令（由设置项提供）。
func StaticPassphrase(value string) PassphraseSource {
	return passphraseFunc(func() (string, error) { return value, nil })
}

// EnvPassphrase 从环境变量读取口令（CI / 脚本场景）。
func EnvPassphrase(envVar string) PassphraseSource {
	return passphraseFunc(func() (string, error) { return os.Getenv(envVar), nil })
}

// NoPassphrase 永远不提供口令：在需要文件后端的环境里，装配会明确失败而不是静默降级。
func NoPassphrase() PassphraseSource {
	return passphraseFunc(func() (string, error) { return "", nil })
}

// ChainPassphrases 按顺序尝试多个来源，取第一个非空结果。
func ChainPassphrases(sources ...PassphraseSource) PassphraseSource {
	return passphraseFunc(func() (string, error) {
		for _, src := range sources {
			if src == nil {
				continue
			}

			value, err := src.Passphrase()
			if err != nil {
				return "", err
			}

			if value != "" {
				return value, nil
			}
		}

		return "", nil
	})
}

// AllowedBackends 返回与上游 cmd/common.go 的 newKeychain **逐字一致**的后端白名单。
//
// 不要「顺手」把 WinCredBackend 加进来：那样 Windows 上 GUI 用凭据管理器、而官方 CLI
// 与我们的 MCP helper（复用上游装配）仍用文件后端，双方看不到彼此登录态，
// 会直接打断「GUI 登录过 → CLI/MCP 可用」这条产品承诺。
func AllowedBackends() []keyring.BackendType {
	return []keyring.BackendType{
		keyring.KeychainBackend,
		keyring.SecretServiceBackend,
		keyring.FileBackend,
	}
}

// NewKeychain 复刻 cmd/common.go 的 newKeychain。
func NewKeychain(stateDirectory string, source PassphraseSource) (keychain.Keychain, error) {
	ring, err := keyring.Open(keyring.Config{
		AllowedBackends:          AllowedBackends(),
		ServiceName:              KeychainServiceName,
		KeychainTrustApplication: true,
		FileDir:                  stateDirectory,
		FilePasswordFunc: func(string) (string, error) {
			if source == nil {
				return "", ErrPassphraseRequired
			}

			value, err := source.Passphrase()
			if err != nil {
				return "", err
			}

			if value == "" {
				return "", ErrPassphraseRequired
			}

			return value, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open credential store: %w", err)
	}

	return keychain.New(keychain.Args{
		Keyring: ring,
		Label:   KeychainServiceName,
	}), nil
}

// PickedBackend 推算 keyring.Open 实际会选哪个后端（它自己不暴露这个信息）。
func PickedBackend() (keyring.BackendType, bool) {
	available := keyring.AvailableBackends()

	for _, allowed := range AllowedBackends() {
		for _, candidate := range available {
			if allowed == candidate {
				return allowed, true
			}
		}
	}

	return keyring.InvalidBackend, false
}

// ExpectedBackend 是我们**期望**落到的后端，用于自检断言（见 selftest.go）。
//
// 关键：macOS 上必须期望 keychain。byteness/keyring 的 keychain_macos.go 带
// `//go:build darwin && !ios && cgo` 标签——一旦用 CGO_ENABLED=0 构建，钥匙串后端
// 会**悄悄消失**并降级成文件后端（表现为「要求设置口令」而不是报错）。自检要把它
// 变成一条明确的失败，而不是让用户去猜。
func ExpectedBackend() keyring.BackendType {
	switch goos() {
	case "darwin":
		return keyring.KeychainBackend
	case "windows":
		return keyring.FileBackend
	default:
		return keyring.FileBackend
	}
}
