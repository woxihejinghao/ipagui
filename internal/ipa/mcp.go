package ipa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpInstructions 是给模型的系统级说明。
//
// 关键点：**本服务端不提供登录工具**。Apple 账号密码一旦作为工具参数传入，
// 就会进入模型的上下文（以及可能被记录的服务端日志）。所以登录只能由人类
// 在 GUI 里完成，模型只能查询状态、然后请人类去登录。
const mcpInstructions = `IPA GUI 的 App Store 工具集。

使用前提：账号已在 IPA GUI 里登录过（与官方 ipatool CLI 共用同一份登录态）。

登录相关：本服务端**故意不提供**登录工具。当 account_status 报告未登录时，
请让用户打开 IPA GUI 完成登录，然后重试；不要索取 Apple 账号或密码。

下载：download_app 只能写入用户在设置里指定的下载目录；其他路径一律拒绝。
购买：仅支持免费应用（Apple 的限制），付费应用会返回错误。`

// MCPServerOptions 是组装 MCP 服务端的参数。
type MCPServerOptions struct {
	// Version 是展示给 MCP 客户端的版本号。
	Version string
	// AllowedOutputDir 限制 download_app 的落点。
	//
	// 为什么要有这个限制：MCP 客户端的参数最终由模型生成，而模型会被页面内容
	// 影响（提示注入）。一个「可以写到任意绝对路径」的下载工具等于把任意文件写入
	// 能力交给模型。这里把落点钉死在一个用户已经选定的目录里。
	AllowedOutputDir string
}

// NewMCPServer 组装 IPA GUI 的 MCP 服务端（stdio 传输由调用方决定）。
func NewMCPServer(services *Services, opts MCPServerOptions) *mcp.Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ipa-gui",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
	})

	tools := &mcpTools{
		services:         services,
		allowedOutputDir: opts.AllowedOutputDir,
		gate:             make(chan struct{}, 1),
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "account_status",
		Description: "Report whether an App Store account is available and, if so, which one. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.accountStatus)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_apps",
		Description: "Search the App Store for apps by name or bundle identifier.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.searchApps)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup_app",
		Description: "Look up a single app by numeric app ID or bundle identifier.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.lookupApp)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_app_versions",
		Description: "List the external version identifiers available for an app.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.listVersions)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_version_metadata",
		Description: "Get the display version and release date for one external version identifier.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.getVersionMetadata)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_purchases",
		Description: "List apps owned by the authenticated account, with pagination.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, tools.listPurchases)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "download_app",
		Description: "Download an app package into the configured download directory. Optionally acquire a free license first. Writes to disk.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
	}, tools.downloadApp)

	return server
}

type mcpTools struct {
	services *Services
	// allowedOutputDir 见 MCPServerOptions.AllowedOutputDir。
	allowedOutputDir string
	// gate 串行化工具调用：AppStore 内部共享账号状态与缓存，并发调用不安全。
	gate chan struct{}
}

// run 串行执行一次工具调用，并在调用前后检查 ctx。
func (t *mcpTools) run(ctx context.Context, name string, action func() error) error {
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return fmt.Errorf("工具调用已取消: %w", ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("工具调用已取消: %w", err)
	}

	if err := action(); err != nil {
		// 带上稳定的错误码，让模型能按 code 决策（而不是抠自然语言）。
		var appErrValue *AppError
		if errors.As(err, &appErrValue) {
			return fmt.Errorf("%s 失败 [%s]: %s", name, appErrValue.Code, appErrValue.Message)
		}

		return fmt.Errorf("%s 失败: %w", name, err)
	}

	return nil
}

// --- 工具输入/输出 ---

type mcpAccountStatusOutput struct {
	LoggedIn   bool   `json:"loggedIn"`
	Email      string `json:"email,omitempty"`
	Name       string `json:"name,omitempty"`
	StoreFront string `json:"storeFront,omitempty"`
	Guidance   string `json:"guidance"`
}

func (t *mcpTools) accountStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpAccountStatusOutput, error) {
	var output mcpAccountStatusOutput

	err := t.run(ctx, "account_status", func() error {
		account, err := t.services.Session().Info()
		if err != nil {
			if IsNotLoggedIn(err) {
				output.LoggedIn = false
				output.Guidance = "尚未登录。请让用户在 IPA GUI 里完成登录后再调用其他工具。"

				return nil
			}

			return err
		}

		output.LoggedIn = true
		output.Email = account.Email
		output.Name = account.Name
		output.StoreFront = account.StoreFront
		output.Guidance = "已登录，可以直接使用其他工具。"

		return nil
	})
	if err != nil {
		return nil, mcpAccountStatusOutput{}, err
	}

	return nil, output, nil
}

type mcpSearchInput struct {
	Term     string `json:"term" jsonschema:"Search term"`
	Limit    *int64 `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 5. visionOS supports up to 12."`
	Platform string `json:"platform,omitempty" jsonschema:"iphone (ios), ipad (ipados), appletv (tvos), watchos, visionos, or macos; defaults to ios+ipados"`
}

func (t *mcpTools) searchApps(ctx context.Context, _ *mcp.CallToolRequest, input mcpSearchInput) (*mcp.CallToolResult, SearchResult, error) {
	var output SearchResult

	err := t.run(ctx, "search_apps", func() error {
		limit := int64(0)
		if input.Limit != nil {
			limit = *input.Limit
		}

		result, err := t.services.Search(ctx, input.Term, limit, input.Platform)
		if err != nil {
			return err
		}

		output = result

		return nil
	})
	if err != nil {
		return nil, SearchResult{}, err
	}

	return nil, output, nil
}

type mcpAppInput struct {
	AppID    int64  `json:"app_id,omitempty" jsonschema:"Numeric app ID; required unless bundle_identifier is provided"`
	BundleID string `json:"bundle_identifier,omitempty" jsonschema:"Bundle identifier; overrides app_id when set"`
	Platform string `json:"platform,omitempty" jsonschema:"iphone (ios), ipad (ipados), appletv (tvos), watchos, visionos, or macos"`
}

func (t *mcpTools) lookupApp(ctx context.Context, _ *mcp.CallToolRequest, input mcpAppInput) (*mcp.CallToolResult, AppView, error) {
	var output AppView

	err := t.run(ctx, "lookup_app", func() error {
		app, err := t.services.Lookup(ctx, input.AppID, input.BundleID, input.Platform)
		if err != nil {
			return err
		}

		output = app

		return nil
	})
	if err != nil {
		return nil, AppView{}, err
	}

	return nil, output, nil
}

func (t *mcpTools) listVersions(ctx context.Context, _ *mcp.CallToolRequest, input mcpAppInput) (*mcp.CallToolResult, VersionList, error) {
	var output VersionList

	err := t.run(ctx, "list_app_versions", func() error {
		versions, err := t.services.ListVersions(ctx, input.AppID, input.BundleID, input.Platform)
		if err != nil {
			return err
		}

		output = versions

		return nil
	})
	if err != nil {
		return nil, VersionList{}, err
	}

	return nil, output, nil
}

type mcpVersionMetadataInput struct {
	mcpAppInput
	ExternalVersionID string `json:"external_version_id" jsonschema:"External version identifier from list_app_versions"`
}

func (t *mcpTools) getVersionMetadata(ctx context.Context, _ *mcp.CallToolRequest, input mcpVersionMetadataInput) (*mcp.CallToolResult, VersionMetadata, error) {
	var output VersionMetadata

	err := t.run(ctx, "get_version_metadata", func() error {
		metadata, err := t.services.GetVersionMetadata(ctx, input.AppID, input.BundleID, input.ExternalVersionID, input.Platform)
		if err != nil {
			return err
		}

		output = metadata

		return nil
	})
	if err != nil {
		return nil, VersionMetadata{}, err
	}

	return nil, output, nil
}

type mcpPurchasesInput struct {
	Page     *int   `json:"page,omitempty" jsonschema:"Page number starting at 1; defaults to 1"`
	Limit    *int   `json:"limit,omitempty" jsonschema:"Results per page; defaults to 10, maximum 100"`
	Platform string `json:"platform,omitempty" jsonschema:"iphone (ios), ipad (ipados), appletv (tvos), watchos, visionos, or macos"`
}

func (t *mcpTools) listPurchases(ctx context.Context, _ *mcp.CallToolRequest, input mcpPurchasesInput) (*mcp.CallToolResult, OwnedAppsPage, error) {
	var output OwnedAppsPage

	err := t.run(ctx, "list_purchases", func() error {
		page := 0
		if input.Page != nil {
			page = *input.Page
		}

		limit := 0
		if input.Limit != nil {
			limit = *input.Limit
		}

		result, err := t.services.ListOwnedApps(ctx, page, limit, input.Platform)
		if err != nil {
			return err
		}

		output = result

		return nil
	})
	if err != nil {
		return nil, OwnedAppsPage{}, err
	}

	return nil, output, nil
}

type mcpDownloadInput struct {
	mcpAppInput
	ExternalVersionID string `json:"external_version_id,omitempty" jsonschema:"External version identifier; defaults to the latest version"`
	Purchase          bool   `json:"purchase,omitempty" jsonschema:"Acquire a free license first if the app is not owned; defaults to false"`
}

type mcpDownloadOutput struct {
	App               AppView `json:"app"`
	DestinationPath   string  `json:"destinationPath"`
	ExternalVersionID string  `json:"externalVersionId,omitempty"`
	AcquiredLicense   bool    `json:"acquiredLicense"`
	SinfCount         int     `json:"sinfCount"`
}

func (t *mcpTools) downloadApp(ctx context.Context, _ *mcp.CallToolRequest, input mcpDownloadInput) (*mcp.CallToolResult, mcpDownloadOutput, error) {
	var output mcpDownloadOutput

	err := t.run(ctx, "download_app", func() error {
		directory := t.allowedOutputDir
		if directory == "" {
			return errors.New("未配置下载目录：请先在 IPA GUI 里选择下载目录")
		}

		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("创建下载目录失败: %w", err)
		}

		app, err := t.services.Lookup(ctx, input.AppID, input.BundleID, input.Platform)
		if err != nil {
			return err
		}

		destination := filepath.Join(directory, DownloadFileName(
			app.ID, app.Name, app.BundleID, input.ExternalVersionID, app.Version,
			t.services.Session().AccountLabel(),
		))

		// 几何判据：解析后的绝对路径必须仍在允许目录内（防 `..` 与符号链接之外的拼接逃逸）。
		if err := ensureWithinDirectory(directory, destination); err != nil {
			return err
		}

		result, err := t.services.Download(ctx, DownloadRequest{
			AppID:             app.ID,
			BundleID:          app.BundleID,
			Platform:          input.Platform,
			ExternalVersionID: input.ExternalVersionID,
			OutputPath:        destination,
			AcquireLicense:    input.Purchase,
		}, nil)
		if err != nil {
			return err
		}

		output = mcpDownloadOutput{
			App:               app,
			DestinationPath:   result.DestinationPath,
			ExternalVersionID: input.ExternalVersionID,
			AcquiredLicense:   result.AcquiredLicense,
			SinfCount:         result.SinfCount,
		}

		return nil
	})
	if err != nil {
		return nil, mcpDownloadOutput{}, err
	}

	return nil, output, nil
}

// ensureWithinDirectory 断言 target 在 directory 之内。
func ensureWithinDirectory(directory, target string) error {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("解析下载目录失败: %w", err)
	}

	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析目标路径失败: %w", err)
	}

	relative, err := filepath.Rel(absoluteDirectory, absoluteTarget)
	if err != nil {
		return fmt.Errorf("目标路径不在下载目录内: %w", err)
	}

	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("目标路径逃出了下载目录: %s", absoluteTarget)
	}

	return nil
}
