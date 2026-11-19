package ipa

import (
	"errors"
	"fmt"

	"github.com/byteness/keyring"
)

const (
	// appSecretServiceName 是**本应用自己的**凭据命名空间。
	//
	// 为什么必须与 ipatool 的 service 名分开：ipatool-auth.service 是上游 CLI
	// 与 MCP helper 共用的命名空间，里面存的是 Apple 账号条目。我们要是往里塞
	// 自己的东西，就可能污染对方的读取（上游 AccountInfo 直接 key="account"）。
	appSecretServiceName = "ipagui.settings"
	appPassphraseKey     = "file-passphrase"
)

// AppSecretStore 用操作系统的凭据库保存**本应用自己**的小秘密。
//
// 目前只有一样：文件后端的口令（ADR-3，Windows 上必然需要）。
//
// 为什么不用文件后端来存这个口令：那是循环依赖（口令加密口令）。
// 所以这里的白名单**故意不含 FileBackend** —— 拿不到系统凭据库时，
// 宁可让用户每次手输，也不把口令明文写到磁盘上。
type AppSecretStore struct {
	ring keyring.Keyring
	err  error
}

// NewAppSecretStore 打开应用级凭据库。失败不返回 error，
// 而是让 Available() 报告 false，调用方据此退回到「每次提示 / 环境变量」。
func NewAppSecretStore() *AppSecretStore {
	ring, err := keyring.Open(keyring.Config{
		AllowedBackends: []keyring.BackendType{
			keyring.KeychainBackend,      // macOS
			keyring.WinCredBackend,       // Windows 凭据管理器
			keyring.SecretServiceBackend, // Linux 桌面
		},
		ServiceName:              appSecretServiceName,
		KeychainTrustApplication: true,
	})

	return &AppSecretStore{ring: ring, err: err}
}

// Available 报告系统凭据库是否可用。
func (s *AppSecretStore) Available() bool {
	if s == nil || s.err != nil || s.ring == nil {
		return false
	}

	// AvailableBackends() 返回的是**本机实际注册**的后端（受 build tag 约束），
	// 与 backendOrder 取交集才能确定 keyring.Open 真的会挑中其中一个。
	available := keyring.AvailableBackends()
	for _, want := range appSecretAllowedBackends() {
		for _, have := range available {
			if want == have {
				return true
			}
		}
	}

	return false
}

func appSecretAllowedBackends() []keyring.BackendType {
	return []keyring.BackendType{
		keyring.KeychainBackend,
		keyring.WinCredBackend,
		keyring.SecretServiceBackend,
	}
}

// Save 保存口令。
func (s *AppSecretStore) Save(passphrase string) error {
	if !s.Available() {
		return errors.New("系统凭据库不可用，无法安全保存口令")
	}

	if passphrase == "" {
		return s.Delete()
	}

	err := s.ring.Set(keyring.Item{
		Key:         appPassphraseKey,
		Data:        []byte(passphrase),
		Label:       "IPA GUI 密钥库口令",
		Description: "用于解密 ipatool 文件后端保存的登录凭据",
	})
	if err != nil {
		return fmt.Errorf("保存口令失败: %w", err)
	}

	return nil
}

// Load 读出口令；(“”, false) 表示没有保存过。
func (s *AppSecretStore) Load() (string, bool) {
	if !s.Available() {
		return "", false
	}

	item, err := s.ring.Get(appPassphraseKey)
	if err != nil {
		return "", false
	}

	return string(item.Data), true
}

// Delete 删除已保存的口令。
func (s *AppSecretStore) Delete() error {
	if !s.Available() {
		return nil
	}

	if err := s.ring.Remove(appPassphraseKey); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) {
		return fmt.Errorf("删除口令失败: %w", err)
	}

	return nil
}

// PassphraseChain 组装文件后端口令的来源优先级：
//
//  1. 环境变量 IPAGUI_KEYCHAIN_PASSPHRASE —— CI / 脚本场景，最高优先级；
//  2. 系统凭据库里保存的口令 —— 用户在设置里填过一次；
//  3. 空 —— 需要口令时装配会**明确失败**（ErrPassphraseRequired），
//     而不是静默降级成一个没加密的文件后端。
func PassphraseChain() PassphraseSource {
	store := NewAppSecretStore()

	return ChainPassphrases(
		EnvPassphrase("IPAGUI_KEYCHAIN_PASSPHRASE"),
		passphraseFunc(func() (string, error) {
			value, ok := store.Load()
			if !ok {
				return "", nil
			}

			return value, nil
		}),
	)
}
