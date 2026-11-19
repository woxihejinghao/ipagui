// Command selfcheck 跑一遍运行时自检并把结论打成 JSON。
//
// 它存在的理由有两个，都是「把静默故障变成看得见的失败」：
//
//  1. 用户报障时，我们要的是这台机器上的**事实**，而不是「大概是什么问题」。
//     这个命令把状态目录、凭据后端、CGO、上游版本一次吐出来。
//
//  2. GUI 是 Wails 应用，双击启动后看不到 stderr。自检逻辑（internal/ipa.Preflight）
//     必须有一条能在**真实二进制**里跑通的验证路径 —— 特别是上游版本检查：
//     go test 构建的测试二进制里 debug.ReadBuildInfo().Deps 是空的（实测 deps=0），
//     所以那条判据在测试里永远拿不到版本，只能在真实二进制里验证。
//
// 用法：
//
//	go run ./cmd/selfcheck          # 人类可读
//	go run ./cmd/selfcheck --json   # 机器可读（报障时贴这个）
//
// 退出码：0 = 全部 ok（warn 不影响），1 = 存在 fail。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/milan/ipagui/internal/ipa"
)

func main() {
	asJSON := flag.Bool("json", false, "以 JSON 输出")
	stateDir := flag.String("state-dir", "", "覆盖状态目录（默认 $HOME/.ipatool 或 XDG 规则）")
	passphrase := flag.String("keychain-passphrase", "", "文件后端口令（Windows 上通常需要）")
	flag.Parse()

	deps, err := ipa.NewDeps(ipa.Options{
		StateDir:   *stateDir,
		Passphrase: ipa.StaticPassphrase(*passphrase),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "装配失败: %v\n", err)

		os.Exit(1)
	}

	report := deps.Preflight()

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "写出 JSON 失败: %v\n", err)

			os.Exit(1)
		}
	} else {
		fmt.Printf("状态目录      : %s\n", report.StateDir)
		fmt.Printf("用户目录      : %s\n", report.HomeDir)
		fmt.Printf("平台          : %s/%s (CGO=%v)\n", report.GOOS, report.GOARCH, report.CGOEnabled)
		fmt.Printf("凭据后端      : %s\n", report.Backend)
		fmt.Printf("上游 ipatool  : %s\n", report.UpstreamVersion)
		fmt.Println("检查项:")

		for _, check := range report.Checks {
			marker := map[ipa.CheckStatus]string{
				ipa.StatusOK:   "✓",
				ipa.StatusWarn: "!",
				ipa.StatusFail: "✗",
			}[check.Status]

			fmt.Printf("  %s %-22s %s\n", marker, check.Name, check.Detail)
		}
	}

	if !report.OK {
		os.Exit(1)
	}
}
