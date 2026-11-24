; IPA GUI 的 Windows 安装包脚本（Inno Setup 6）。
;
; 用法（在仓库根目录）：
;   ISCC.exe /DAppVersion=0.1.1 scripts\installer\ipagui.iss
; 输入是 scripts\build-windows.ps1 产出的 dist\ipagui-windows-amd64\，
; 输出是 dist\IPAGUI-<版本>-setup.exe。
;
; 两个文件必须装在同一个目录：GUI 的 MCP 客户端配置要写 ipa-mcp.exe 的绝对路径
; （architecture.md §8.11），少一个文件等于少一半功能。

#ifndef AppVersion
  #define AppVersion "0.1.1"
#endif

#define AppName "IPA GUI"
#define AppExeName "IPAGUI.exe"
#define AppPublisher "ipagui contributors"
#define AppURL "https://github.com/woxihejinghao/ipagui"
#define RootDir "..\.."
#define PayloadDir RootDir + "\dist\ipagui-windows-amd64"

[Setup]
; AppId 是这台机器上的产品身份，改动它会让旧版本无法被覆盖升级 —— 不要动。
AppId={{F008A2AC-1162-4274-96DB-7D41617461B7}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}/issues
AppUpdatesURL={#AppURL}/releases
VersionInfoVersion={#AppVersion}
DefaultDirName={autopf}\IPAGUI
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
; 默认按当前用户安装（不需要管理员权限）；想装到 Program Files 可以在欢迎页切换。
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir={#RootDir}\dist
OutputBaseFilename=IPAGUI-{#AppVersion}-setup
SetupIconFile={#RootDir}\build\windows\icon.ico
UninstallDisplayIcon={app}\{#AppExeName}
UninstallDisplayName={#AppName}
LicenseFile={#RootDir}\LICENSE
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; 应用自身会写 %LOCALAPPDATA%\ipatool 等用户数据，卸载时不能连带删除。
CloseApplications=yes

[Languages]
Name: "zh"; MessagesFile: "ChineseSimplified.isl"
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#PayloadDir}\IPAGUI.exe";  DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\ipa-mcp.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#RootDir}\README.zh-CN.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "{#RootDir}\README.md";      DestDir: "{app}\docs"; Flags: ignoreversion
Source: "{#RootDir}\NOTICE";         DestDir: "{app}\docs"; Flags: ignoreversion
Source: "{#RootDir}\LICENSE";        DestDir: "{app}\docs"; Flags: ignoreversion
Source: "{#RootDir}\third_party\ipatool\LICENSE"; DestDir: "{app}\docs"; DestName: "LICENSE-ipatool"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExeName}"
Name: "{group}\{cm:UninstallProgram,{#AppName}}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExeName}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent

[Code]
// Win10 1809 之前没有 WebView2 运行时，装了也只会是一个白窗口；
// 这里只提示、不阻断（用户仍然可以自己装 WebView2 后使用）。
function InitializeSetup(): Boolean;
var
  WebView2Version: String;
begin
  Result := True;
  if not RegQueryStringValue(HKLM32, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', WebView2Version) then
  begin
    if not RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', WebView2Version) then
      MsgBox('没有检测到 Microsoft Edge WebView2 运行时。' + #13#10 +
             'IPA GUI 的界面依赖它，请先安装 WebView2（Windows 11 一般已自带）。' + #13#10 + #13#10 +
             '安装仍会继续，但界面可能无法显示。', mbInformation, MB_OK);
  end;
end;