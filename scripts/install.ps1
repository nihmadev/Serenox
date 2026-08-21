[CmdletBinding()]
param(
    [string]$Version,
    [string]$InstallDir,
    [switch]$NoPlugin
)

$ErrorActionPreference = 'Stop'

function Stop-Installer([string]$Message) {
    throw "serenox installer: $Message"
}

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = if ($env:SERENOX_VERSION) { $env:SERENOX_VERSION } else { 'latest' }
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = if ($env:SERENOX_INSTALL_DIR) {
        $env:SERENOX_INSTALL_DIR
    } else {
        $localAppData = if ($env:LOCALAPPDATA) { $env:LOCALAPPDATA } else { Join-Path $HOME 'AppData/Local' }
        Join-Path $localAppData 'Serenox/bin'
    }
}
$repo = if ($env:SERENOX_REPO) { $env:SERENOX_REPO } else { 'nihmadev/Serenox' }
$base = if ($env:SERENOX_RELEASE_BASE_URL) {
    $env:SERENOX_RELEASE_BASE_URL.TrimEnd('/')
} else {
    "https://github.com/$repo/releases"
}
$installPlugin = -not $NoPlugin
if ($env:SERENOX_INSTALL_PLUGIN -eq '0') { $installPlugin = $false }

if ($env:SERENOX_RELEASE_BASE_URL -and $base -notmatch '^(https://|file://)') {
    Stop-Installer 'SERENOX_RELEASE_BASE_URL must use https:// or explicit file:// test source'
}

$architecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$architecture = $architecture.ToUpperInvariant()
switch ($architecture) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { Stop-Installer "unsupported Windows architecture: $architecture" }
}
$archiveName = "serenox_windows_${arch}.zip"
$suffix = if ($Version -eq 'latest') { 'latest/download' } else { "download/$Version" }
$archiveUri = "$base/$suffix/$archiveName"
$checksumUri = "$base/$suffix/checksums.txt"

function Get-ReleaseFile([string]$Uri, [string]$Destination) {
    if ($Uri.StartsWith('file://')) {
        $source = ([Uri]$Uri).LocalPath
        Copy-Item -LiteralPath $source -Destination $Destination -Force
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $Destination
    }
}

$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("serenox-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null
try {
    $archive = Join-Path $tempDir $archiveName
    $checksums = Join-Path $tempDir 'checksums.txt'
    Get-ReleaseFile $archiveUri $archive
    Get-ReleaseFile $checksumUri $checksums

    $line = Get-Content -LiteralPath $checksums | Where-Object {
        $fields = ($_ -split '\s+')
        $fields.Count -ge 2 -and $fields[-1] -eq $archiveName
    } | Select-Object -First 1
    if (-not $line) { Stop-Installer "checksums.txt does not contain $archiveName" }
    $expected = ($line -split '\s+')[0].ToLowerInvariant()
    if ($expected -notmatch '^[0-9a-f]{64}$') { Stop-Installer 'invalid release checksum' }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { Stop-Installer "checksum mismatch for $archiveName" }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archive)
    try {
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName.Replace('\', '/')
            if ([IO.Path]::IsPathRooted($name) -or $name -match '(^|/)\.\.(\/|$)') {
                Stop-Installer "archive contains an unsafe path: $name"
            }
        }
    } finally {
        $zip.Dispose()
    }

    $extractDir = Join-Path $tempDir 'extracted'
    Expand-Archive -LiteralPath $archive -DestinationPath $extractDir -Force
    $names = @('serenox.exe', 'serenoxd.exe')
    if ($installPlugin) { $names += 'serenox-plugin.exe' }
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    foreach ($name in $names) {
        $source = Join-Path $extractDir $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            Stop-Installer "release archive is missing $name"
        }
        $destination = Join-Path $InstallDir $name
        $temporary = "$destination.tmp.$PID"
        Copy-Item -LiteralPath $source -Destination $temporary -Force
        Move-Item -LiteralPath $temporary -Destination $destination -Force
        Write-Output "installed $destination"
    }
    Write-Output "Serenox $Version installed for windows/$arch"
    if (($env:Path -split ';') -notcontains $InstallDir) {
        Write-Output "Add $InstallDir to PATH to invoke serenox directly."
    }
} finally {
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
}
