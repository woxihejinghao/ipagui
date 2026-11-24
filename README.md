# IPA GUI

**English** · **[简体中文](README.zh-CN.md)**

A Wails desktop GUI for downloading IPAs of App Store apps **you already own**,
built on top of [ipatool](https://github.com/majd/ipatool).

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go](https://img.shields.io/badge/go-1.26.5-00ADD8.svg)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Windows-lightgrey.svg)

<img src="docs/screenshots/search.png" width="860" alt="Search view">
<img src="docs/screenshots/settings.png" width="860" alt="Settings view">

---

## What it is

`ipagui` wraps the Go business layer of ipatool (`pkg/appstore`) in a native
desktop application, so the whole flow is a few clicks instead of a chain of
CLI commands:

- sign in to the App Store with your own Apple ID (2FA supported)
- search the store, inspect app details and version history
- download an IPA with resumable transfer and byte-accurate progress
- keep a local SQLite index of what you downloaded, with hashes
- optionally expose the same capabilities as an MCP server (`cmd/ipa-mcp`)

Backend: Go + Wails v2. Frontend: React 19 + Vite. macOS and Windows.

## Requirements

| | |
|---|---|
| Go | 1.26.5 or newer (pinned by `go.mod`) |
| Wails CLI | v2 (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`) |
| Node.js | 20+ (for the frontend build) |
| macOS | Xcode Command Line Tools |
| Windows | WebView2 runtime (preinstalled on Windows 11) |

## Build

```bash
# 1. frontend deps
cd frontend && npm ci && cd ..

# 2. build (frontend is built automatically)
wails build
```

Platform helper scripts:

```bash
./scripts/build-macos.sh            # macOS, arm64 by default; pass amd64 for Intel
pwsh scripts/build-windows.ps1      # Windows, amd64 by default; pass arm64
```

### Note on `go:embed`

`main.go` embeds the frontend build output:

```go
//go:embed all:frontend/dist
var assets embed.FS
```

Only `frontend/dist/.gitkeep` is tracked. A fresh clone therefore *compiles*
with a bare `go build` — but the embedded assets are empty and the app opens a
blank window. Run `wails build` (it builds the frontend first), or
`npm ci && npm run build` inside `frontend/`, before `go build`, to get a
usable binary.

`npm run build` (and therefore `wails build`) wipes `frontend/dist`, including
the tracked `.gitkeep`. The platform build scripts restore it automatically;
if you build the frontend by hand, run `git checkout -- frontend/dist/.gitkeep`
afterwards to keep the work tree clean.

## Usage limits — please read

This tool is for **personal backup of apps you have already purchased with your
own Apple ID**. It is not:

- a way to obtain apps you have not paid for;
- an account provider — it never ships or shares Apple IDs;
- a redistribution tool for IPAs;
- a way around App Store purchase, licensing, or DRM checks.

Known risks, stated plainly:

- Automating App Store sign-in **can get your Apple ID disabled**. Upstream
  reports: [ipatool#584](https://github.com/majd/ipatool/issues/584),
  [ipatool#627](https://github.com/majd/ipatool/issues/627). Use a secondary
  account if you care about the primary one.
- Downloaded IPAs remain FairPlay-encrypted App Store binaries. They are not
  decrypted by this project.
- No warranty of any kind. See `LICENSE`.

## Where data lives

| Path | Contents |
|---|---|
| `~/.ipatool/` | Login state (cookies, account files), **shared with the official `ipatool` CLI** |
| `~/.ipatool/` keychain item `ipatool-auth.service` | Credentials, shared with the official CLI |
| `~/Library/Application Support/ipagui/settings.json` (macOS) | App settings |
| `%AppData%\ipagui\settings.json` (Windows) | App settings |
| `~/Library/Application Support/ipagui/library.db` (macOS) | Local download index (SQLite) |
| `%AppData%\ipagui\library.db` (Windows) | Local download index (SQLite) |
| `~/Downloads/IPA` | Default download directory (configurable) |

### Credential storage

- **macOS**: the system Keychain. App settings (including the download
  directory and the file-passphrase reference) live under the service name
  `ipagui.settings`.
- **Windows**: ipatool's **file backend**, *not* Windows Credential Manager.
  Credentials are protected by a passphrase the app asks you to set, stored in
  the `ipagui.settings` namespace. This is a deliberate upstream constraint, not
  an oversight.

## Relation to upstream ipatool

`third_party/ipatool/` is a modified copy of
[majd/ipatool](https://github.com/majd/ipatool) v2.8.1, wired in via a Go
`replace` directive:

```text
replace github.com/majd/ipatool/v2 => ./third_party/ipatool
```

Local patches are documented in
[`third_party/ipatool/FORK.md`](third_party/ipatool/FORK.md):

1. a FairPlay fallback so a download never produces an IPA whose binary is
   encrypted but carries no valid `sinf`;
2. an Apple FCS key `User-Agent` header (defensive; the restore-image
   `User-Agent` itself ships upstream since v2.8.1).

The directory contains ~6.7 MB of upstream-generated asset data
(`internal/store/profile/data_generated.go`). It comes from upstream's pinned
inputs, not from this project — see `NOTICE`.

## License

MIT. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE) for third-party
attribution (`third_party/ipatool` is MIT, © Majd Alfhaily) and dependency
license notes.

## Contact

Please open an issue in this repository.
