// Command ipa-mcp 是 IPA GUI 的 MCP helper：一个独立的 stdio 进程。
//
// 为什么必须是**独立进程**（ADR-6）：GUI 进程内直接调上游的 cmd.Execute()
// 会初始化它那一套包级 dependencies 并重定向全局 logger，把 GUI 自己的日志
// 与状态搞乱。MCP 客户端（Claude Desktop / Cursor）本来就是按「启动一个命令
// 并通过 stdin/stdout 通信」的方式工作的，独立进程既符合协议也隔离了状态。
//
// 记住一条硬约束：**这个进程的 stdout 只属于 JSON-RPC**。
// 任何调试输出都必须走 stderr，否则 MCP 客户端会解析失败。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/milan/ipagui/internal/ipa"
)

// version 由构建期注入（-ldflags "-X main.version=..."），默认 dev。
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	downloadDir := flag.String("download-dir", "", "限制下载落点（默认取应用设置里的输出目录）")
	stateDir := flag.String("state-dir", "", "ipatool 状态目录（默认 $HOME/.ipatool，与官方 CLI 共用）")
	keychainPassphrase := flag.String("keychain-passphrase", "", "文件后端专用；一般不用给，默认读系统凭据库或环境变量 IPAGUI_KEYCHAIN_PASSPHRASE")
	selftest := flag.Bool("selftest", false, "只跑环境自检并把结果打到 stdout（非 MCP 模式）")
	flag.Parse()

	passphrase := ipa.PassphraseChain()
	if *keychainPassphrase != "" {
		passphrase = ipa.StaticPassphrase(*keychainPassphrase)
	}

	deps, err := ipa.NewDeps(ipa.Options{StateDir: *stateDir, Passphrase: passphrase})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		return 1
	}

	if *selftest {
		report := deps.Preflight()

		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化自检结果失败: %v\n", err)
			return 1
		}

		fmt.Println(string(encoded))

		if !report.OK {
			return 1
		}

		return 0
	}

	directory := *downloadDir
	if directory == "" {
		// 设置读不到就用默认值：MCP helper 不该因为设置文件缺失而拒绝启动，
		// 因为只读工具（search/lookup）完全不需要下载目录。
		if settings, err := ipa.LoadSettings(); err == nil {
			directory = settings.OutputDir
		} else {
			directory = ipa.DefaultSettings().OutputDir
		}
	}

	server := ipa.NewMCPServer(ipa.NewServices(deps), ipa.MCPServerOptions{
		Version:          version,
		AllowedOutputDir: directory,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "MCP 服务退出: %v\n", err)
		return 1
	}

	return 0
}
