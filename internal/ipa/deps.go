package ipa

import (
	"fmt"
	"path/filepath"

	cookiejar "github.com/juju/persistent-cookiejar"
	"github.com/majd/ipatool/v2/pkg/appstore"
	mshttp "github.com/majd/ipatool/v2/pkg/http"
	"github.com/majd/ipatool/v2/pkg/keychain"
	"github.com/majd/ipatool/v2/pkg/util/machine"
	"github.com/majd/ipatool/v2/pkg/util/operatingsystem"
)

// Options 是装配选项。
type Options struct {
	// StateDir 覆盖状态目录（测试用）。为空时按上游规则解析（$HOME/.ipatool 或 XDG）。
	StateDir string
	// Passphrase 为文件后端提供口令；macOS 走钥匙串时用不到。
	Passphrase PassphraseSource
}

// Deps 是一次装配出的全部依赖，等价于上游 cmd 包里的包级 `dependencies`。
//
// 上游把它做成包级单例（`var dependencies = Dependencies{}`），我们做成显式结构体，
// 这样多个会话/测试可以互不干扰。
type Deps struct {
	OS        operatingsystem.OperatingSystem
	Machine   machine.Machine
	CookieJar mshttp.CookieJar
	// Jar 是 CookieJar 的具体实现（juju 持久 jar）。
	//
	// 为什么单独留一份：换号时要把其它账号的 cookie 剪掉，而 AllCookies /
	// RemoveCookie / Save 都在具体类型上，不在 http.CookieJar 接口里。
	Jar      *cookiejar.Jar
	Keychain keychain.Keychain
	Store    appstore.AppStore

	StateDir string
	HomeDir  string
}

// NewDeps 复刻 cmd/common.go 的 initWithCommand 装配流程。
//
// 与上游的差异：不传 ActionSignerFactory —— appstore.NewAppStore 在它为 nil 时会
// 使用包内 defaultActionSignerFactory（pkg/appstore/action_signer.go:25），
// 所以外部调用方无需自备签名器。
func NewDeps(opts Options) (*Deps, error) {
	osys := operatingsystem.New()
	host := machine.New(machine.Args{OS: osys})
	home := host.HomeDirectory()

	stateDir := opts.StateDir
	if stateDir == "" {
		resolved, err := PrepareStateDirectory(osys, home)
		if err != nil {
			return nil, err
		}

		stateDir = resolved
	} else if err := osys.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create state directory %s: %w", stateDir, err)
	}

	jar, err := cookiejar.New(&cookiejar.Options{
		Filename: filepath.Join(stateDir, CookieJarFileName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open cookie jar: %w", err)
	}

	kc, err := NewKeychain(stateDir, opts.Passphrase)
	if err != nil {
		return nil, err
	}

	return &Deps{
		OS:        osys,
		Machine:   host,
		CookieJar: jar,
		Jar:       jar,
		Keychain:  kc,
		Store: appstore.NewAppStore(appstore.Args{
			CookieJar:       jar,
			OperatingSystem: osys,
			Keychain:        kc,
			Machine:         host,
		}),
		StateDir: stateDir,
		HomeDir:  home,
	}, nil
}

// CookieJarFilePath 返回 cookie jar 文件路径（诊断用；该文件等价于会话凭据）。
func (d *Deps) CookieJarFilePath() string {
	return filepath.Join(d.StateDir, CookieJarFileName)
}
