$ErrorActionPreference = 'Stop'

$repo = if ($env:GLORP_REPO) { $env:GLORP_REPO } else { 'lsegal/glorp' }
$version = if ($env:GLORP_VERSION) { $env:GLORP_VERSION } else { 'latest' }
$installDir = if ($env:GLORP_BIN_DIR) { $env:GLORP_BIN_DIR } else { Join-Path $HOME 'AppData\Local\glorp' }

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
    throw 'gh CLI is required: https://cli.github.com/'
}
if (-not (Get-Command npx -ErrorAction SilentlyContinue)) {
    throw 'Node.js and npx are required to install gh-fix from skills.sh'
}

if ($version -eq 'latest') {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
    $version = $release.tag_name
}
$tag = $version
$version = $version.TrimStart('v')
$architecture = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
$archive = "glorp_${version}_windows_${architecture}.zip"
$url = "https://github.com/$repo/releases/download/$tag/$archive"
$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("glorp-" + [guid]::NewGuid())
$zip = Join-Path $temp $archive
New-Item -ItemType Directory -Path $temp | Out-Null
try {
    Invoke-WebRequest -Uri $url -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $temp -Force
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item (Join-Path $temp 'glorp.exe') (Join-Path $installDir 'glorp.exe') -Force
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $userPath) { $userPath = '' }
    if (($userPath -split ';') -notcontains $installDir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $installDir).Trim(';')), 'User')
    }
    & npx --yes skills add "$repo@gh-fix" --global --agent codex --agent claude-code -y
    Write-Host "Installed glorp to $installDir\glorp.exe and gh-fix globally."
} finally {
    Remove-Item $temp -Recurse -Force -ErrorAction SilentlyContinue
}
