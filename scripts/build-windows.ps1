# 构建 Windows 版 IPA GUI（含 MCP helper）。
#
# 用法（在 Windows 上，或任意平台交叉构建）：
#   pwsh scripts/build-windows.ps1            # 默认 amd64
#   pwsh scripts/build-windows.ps1 arm64
#
# 注意这里刻意不开 cgo：
#   - macOS 的系统钥匙串后端带 cgo 标签，Windows 用不到；
#   - Windows 上凭据走 ipatool 自己的「文件后端」（上游白名单里没有 WinCred），
#     纯 Go 即可，因而可以零工具链交叉构建。
$ErrorActionPreference = "Stop"

# 外部命令（go / wails）会把进度写到 stderr。Windows PowerShell 5.1 在
# $ErrorActionPreference = "Stop" 时把原生命令的 stderr 当成终止错误，脚本会在
# 「构建应用」那一步当场中断 —— 看着像构建失败，其实进程刚跑一半。所以外呼一律
# 走这个包装：临时放宽偏好，成败只认 $LASTEXITCODE。
function Invoke-Native {
    param([scriptblock]$Command)

    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        # 2>&1 之后再逐条当文本写出去：否则 5.1 会把每行 stderr 渲染成红色
        # NativeCommandError，看着像出错，其实只是 wails 的进度信息。
        & $Command 2>&1 | ForEach-Object {
            if ($_ -is [System.Management.Automation.ErrorRecord]) {
                Write-Host $_.Exception.Message
            } else {
                Write-Host $_
            }
        }
    } finally {
        $ErrorActionPreference = $previous
    }
}

$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

$Arch = if ($args.Count -ge 1) { $args[0] } else { "amd64" }
if ($Arch -ne "amd64" -and $Arch -ne "arm64") {
    throw "不支持的架构: ${Arch}（仅支持 amd64 / arm64）"
}

$Version = if ($env:VERSION) { $env:VERSION } else { "0.1.0-dev" }

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $Arch

Write-Host "==> 1/5 单测"
Invoke-Native { go test ./internal/... }
if ($LASTEXITCODE -ne 0) { throw "单测失败" }

Write-Host "==> 2/5 静态检查"
Invoke-Native { go vet ./internal/... ./cmd/... }
if ($LASTEXITCODE -ne 0) { throw "go vet 失败" }

Write-Host "==> 3/5 构建 helper（windows/${Arch}）"
Invoke-Native { go build -trimpath -ldflags "-s -w -X main.version=$Version" -o "build\bin\ipa-mcp.exe" ./cmd/ipa-mcp }
if ($LASTEXITCODE -ne 0) { throw "helper 构建失败" }

Write-Host "==> 4/5 构建应用（windows/${Arch}）"
Invoke-Native { wails build -platform "windows/$Arch" -ldflags "-X main.version=$Version" }
if ($LASTEXITCODE -ne 0) { throw "应用构建失败" }

Write-Host "==> 5/5 组装发布目录"
$Dist = "dist\ipagui-windows-$Arch"
New-Item -ItemType Directory -Force -Path $Dist | Out-Null
Copy-Item "build\bin\IPAGUI.exe"   "$Dist\" -Force
Copy-Item "build\bin\ipa-mcp.exe"  "$Dist\" -Force

Write-Host ""
Write-Host "ok: $Dist"
Get-ChildItem $Dist | Format-Table Name, Length
Write-Host "ipa-mcp.exe 必须与 IPAGUI.exe 同目录：GUI 里已没有「一键接入」，客户端配置要手写这个路径（architecture.md §8.11）。"

# vite 的 emptyOutDir 会删掉 frontend/dist/.gitkeep；补回去，避免 git status 变脏。
if (Test-Path "frontend\dist") { New-Item -ItemType File -Force -Path "frontend\dist\.gitkeep" | Out-Null }
