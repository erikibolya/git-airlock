#define AppName "Git Airlock"
#ifndef AppVersion
  #define AppVersion "1.4.0"
#endif
#define AppPublisher "Erik Ibolya"
#define AppExe "airlock-desktop.exe"
#define AppURL "https://github.com/erikibolya/git-airlock"

[Setup]
AppId={{A3F1B6C2-9D4E-4B7A-8C15-6E2D9F0A1B34}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}
AppUpdatesURL={#AppURL}
DefaultDirName={localappdata}\Programs\Git Airlock
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
OutputDir=dist
OutputBaseFilename=GitAirlockSetup-{#AppVersion}
SetupIconFile=files\appicon.ico
UninstallDisplayIcon={app}\{#AppExe}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0

[Languages]
Name: "czech"; MessagesFile: "compiler:Languages\Czech.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "files\airlock.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "files\airlock-desktop.exe"; DestDir: "{app}"; Flags: ignoreversion
; The active config lives next to the executables. Shortcuts start in {app}, so
; the desktop app finds it without --config. Reinstalls keep the existing file.
Source: "files\airlock.toml"; DestDir: "{app}"; DestName: ".airlock.toml"; Flags: onlyifdoesntexist
Source: "files\airlock.toml.example"; DestDir: "{app}"; Flags: ignoreversion
Source: "files\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "files\LICENSE"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Git Airlock"; Filename: "{app}\{#AppExe}"; WorkingDir: "{app}"; Comment: "Git Airlock dashboard"; AppUserModelID: "Git Airlock"
Name: "{autodesktop}\Git Airlock"; Filename: "{app}\{#AppExe}"; WorkingDir: "{app}"; AppUserModelID: "Git Airlock"
; Starts in the notification area after sign-in. The single-instance lock brings
; an already running window to the front when the Start menu shortcut is used.
Name: "{userstartup}\Git Airlock"; Filename: "{app}\{#AppExe}"; Parameters: "--tray"; WorkingDir: "{app}"; AppUserModelID: "Git Airlock"

[Run]
Filename: "{app}\airlock.exe"; Parameters: "install-task --config ""{app}\.airlock.toml"" --name ""Git Airlock"" --interval 1m"; Flags: runhidden waituntilterminated; StatusMsg: "Installing the background worker task..."
Filename: "{app}\{#AppExe}"; Description: "Start Git Airlock"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{sys}\schtasks.exe"; Parameters: "/delete /tn ""Git Airlock"" /f"; Flags: runhidden waituntilterminated; RunOnceId: "RemoveWorkerTask"
Filename: "{sys}\taskkill.exe"; Parameters: "/IM airlock-desktop.exe /F"; Flags: runhidden waituntilterminated; RunOnceId: "StopDesktop"

[UninstallDelete]
Type: files; Name: "{app}\.airlock.toml"
Type: files; Name: "{localappdata}\git-airlock\worker.vbs"
Type: files; Name: "{localappdata}\git-airlock\worker-task.xml"
Type: files; Name: "{userappdata}\git-airlock\desktop.json"

[Code]
procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssInstall then
  begin
    // Replace a running copy without failing on a locked executable.
    Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM airlock-desktop.exe /F', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Exec(ExpandConstant('{sys}\schtasks.exe'), '/end /tn "Git Airlock"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;
