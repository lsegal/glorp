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
    $installedExe = Join-Path $installDir 'glorp.exe'
    if (Test-Path $installedExe) {
        # Windows won't let us overwrite a running glorp.exe in place, but it
        # will let us rename one out of the way, so upgrading a running
        # instance still succeeds.
        $backupExe = Join-Path $installDir 'glorp.exe.bak'
        Remove-Item $backupExe -Force -ErrorAction SilentlyContinue
        Rename-Item $installedExe 'glorp.exe.bak' -Force
    }
    Copy-Item (Join-Path $temp 'glorp.exe') (Join-Path $installDir 'glorp.exe') -Force
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $userPath) { $userPath = '' }
    if (($userPath -split ';') -notcontains $installDir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $installDir).Trim(';')), 'User')
    }
    # Which agents the skills are installed for comes from the agent registry
    # in the binary just installed, so adding an agent definition never means
    # editing this script.
    $targets = @(& (Join-Path $installDir 'glorp.exe') agents -skills 2>$null)
    if ($LASTEXITCODE -ne 0) { $targets = @() }
    $agentFlags = @()
    foreach ($target in $targets) {
        $target = "$target".Trim()
        if ($target) { $agentFlags += @('--agent', $target) }
    }
    if ($agentFlags.Count -eq 0) {
        Write-Host "Installed glorp $tag to $installDir\glorp.exe."
        throw "Could not read the agent list from glorp, so gh-fix/gh-discuss were not installed. Install them with: npx skills add $repo@gh-fix --global --agent <agent> -y"
    }
    & npx --yes skills add "$repo@gh-fix" --global @agentFlags -y
    & npx --yes skills add "$repo@gh-discuss" --global @agentFlags -y
    Write-Host "Installed glorp $tag to $installDir\glorp.exe and gh-fix/gh-discuss globally."
} finally {
    Remove-Item $temp -Recurse -Force -ErrorAction SilentlyContinue
}
