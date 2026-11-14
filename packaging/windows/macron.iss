#ifndef Version
  #define Version "0.0.0"
#endif

[Setup]
AppId={{7D4C9B2E-5A61-4F0B-9E3D-6C1A2B8F4E57}
AppName=Macron
AppVersion={#Version}
AppVerName=Macron {#Version}
AppPublisher=Metixpro
AppPublisherURL=https://github.com/Metixpro/Macron-Agent
AppSupportURL=https://github.com/Metixpro/Macron-Agent/issues
AppUpdatesURL=https://github.com/Metixpro/Macron-Agent/releases
VersionInfoVersion={#Version}
DefaultDirName={localappdata}\Programs\Macron
DisableProgramGroupPage=yes
DisableDirPage=auto
DisableWelcomePage=no
PrivilegesRequired=lowest
ArchitecturesAllowed=x86compatible x64compatible arm64
ArchitecturesInstallIn64BitMode=x64compatible arm64
MinVersion=10.0
OutputDir=out
OutputBaseFilename=Macron-Setup-{#Version}
SetupIconFile=macron.ico
UninstallDisplayIcon={app}\macron-desktop.exe
UninstallDisplayName=Macron
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
CloseApplications=yes
RestartApplications=no
ChangesEnvironment=yes

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
WelcomeLabel2=This installs Macron {#Version}, your AI agent for this PC: the desktop app and the terminal app.%n%nIt picks the right build for this PC by itself (64-bit, 32-bit or ARM) and needs no administrator rights.

[Tasks]
Name: "desktopicon"; Description: "Put Macron on the desktop"; GroupDescription: "Shortcuts:"
Name: "addtopath"; Description: "Let any terminal run the macron command"; GroupDescription: "Terminal:"

[Files]
Source: "bin\arm64\macron.exe"; DestDir: "{app}"; Check: UseArm64; Flags: ignoreversion
Source: "bin\arm64\macron-desktop.exe"; DestDir: "{app}"; Check: UseArm64; Flags: ignoreversion
Source: "bin\amd64\macron.exe"; DestDir: "{app}"; Check: UseX64; Flags: ignoreversion
Source: "bin\amd64\macron-desktop.exe"; DestDir: "{app}"; Check: UseX64; Flags: ignoreversion
Source: "bin\386\macron.exe"; DestDir: "{app}"; Check: UseX86; Flags: ignoreversion
Source: "bin\386\macron-desktop.exe"; DestDir: "{app}"; Check: UseX86; Flags: ignoreversion
Source: "extra\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\Macron"; Filename: "{app}\macron-desktop.exe"; Comment: "Your AI agent"
Name: "{autoprograms}\Macron Terminal"; Filename: "{app}\macron.exe"; Comment: "Macron in a terminal"
Name: "{autodesktop}\Macron"; Filename: "{app}\macron-desktop.exe"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsAddPath(ExpandConstant('{app}'))

[Run]
Filename: "{app}\macron-desktop.exe"; Description: "Open Macron now"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM macron-desktop.exe"; Flags: runhidden; RunOnceId: "StopDesktop"
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM macron.exe"; Flags: runhidden; RunOnceId: "StopTerminal"

[Code]
function UseArm64: Boolean;
begin
  Result := IsArm64;
end;

function UseX64: Boolean;
begin
  Result := Is64BitInstallMode and not IsArm64;
end;

function UseX86: Boolean;
begin
  Result := not Is64BitInstallMode;
end;

function NormalizedPathEntry(Value: string): string;
begin
  Value := Trim(Value);
  if Length(Value) >= 2 then
    if (Value[1] = '"') and (Value[Length(Value)] = '"') then
      Value := Copy(Value, 2, Length(Value) - 2);
  StringChangeEx(Value, '/', '\', True);
  while Length(Value) > 3 do
  begin
    if Value[Length(Value)] <> '\' then break;
    Delete(Value, Length(Value), 1);
  end;
  Result := Uppercase(Value);
end;

function NeedsAddPath(Dir: string): Boolean;
var
  Paths, Entry: string;
  Sep: Integer;
begin
  Result := True;
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Paths) then exit;
  while Paths <> '' do
  begin
    Sep := Pos(';', Paths);
    if Sep = 0 then Sep := Length(Paths) + 1;
    Entry := Copy(Paths, 1, Sep - 1);
    Delete(Paths, 1, Sep);
    if NormalizedPathEntry(Entry) = NormalizedPathEntry(Dir) then
    begin
      Result := False;
      exit;
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Paths, Dir, Entry, Kept: string;
  Sep: Integer;
begin
  if CurUninstallStep <> usPostUninstall then
    exit;
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Paths) then
    exit;
  Dir := ExpandConstant('{app}');
  Kept := '';
  while Paths <> '' do
  begin
    Sep := Pos(';', Paths);
    if Sep = 0 then Sep := Length(Paths) + 1;
    Entry := Copy(Paths, 1, Sep - 1);
    Delete(Paths, 1, Sep);
    if NormalizedPathEntry(Entry) <> NormalizedPathEntry(Dir) then
    begin
      if Kept <> '' then Kept := Kept + ';';
      Kept := Kept + Entry;
    end;
  end;
  Paths := Kept;
  RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Paths);
end;
