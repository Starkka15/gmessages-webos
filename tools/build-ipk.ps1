# Build ONE installable .ipk that carries everything: the pairing app, the relay
# binary, the account description, and the boot job. Installing it stages all of
# them and starts the relay; removing it (pmPreRemove.script) takes them back out.
# No device needed. The relay must be cross-built first (tools say how, or pass
# -Build to do it here).
#
#   .\tools\build-ipk.ps1            package what is already built
#   .\tools\build-ipk.ps1 -Build     cross-build the relay first, then package
param([switch]$Build)
$ErrorActionPreference = "Stop"
$env:JAVA_HOME = "G:\Java\jdk17"
$env:Path = "G:\Java\jdk17\bin;G:\webOS\PalmSDK\Current\bin;" + $env:Path
$root = Split-Path $PSScriptRoot -Parent
$buildDir = Join-Path $root "build"
$bin = Join-Path $root "relay\build\gmrelay"

if ($Build) {
    Push-Location (Join-Path $root "relay")
    $env:GOPATH = "G:\GoPath"; $env:GOCACHE = "G:\GoPath\cache"; $env:GOFLAGS = "-mod=mod"
    $env:CGO_ENABLED = "0"; $env:GOOS = "linux"; $env:GOARCH = "arm"; $env:GOARM = "7"
    & G:\Go\bin\go.exe build -trimpath -ldflags "-s -w" -o build\gmrelay .
    Pop-Location
}
if (-not (Test-Path $bin)) { Write-Output "RESULT: FAILED (relay not built; run with -Build)"; exit 1 }

New-Item -ItemType Directory -Force $buildDir | Out-Null

# Stage a copy of the app with the extra payload bundled inside it, so palm-package
# (which only packs the app tree) carries all of it. The real app\ is left alone.
$stage = Join-Path $buildDir "appstage"
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force $stage | Out-Null
Copy-Item (Join-Path $root "app\*") $stage -Recurse
Copy-Item $bin (Join-Path $stage "gmrelay")
Copy-Item (Join-Path $root "webos\account") (Join-Path $stage "account") -Recurse
Copy-Item (Join-Path $root "webos\com.palm.stark.gmrelay") (Join-Path $stage "gmrelay.upstart")

Push-Location $buildDir
Remove-Item "com.palm.stark.gmessages_*_all.ipk" -ErrorAction SilentlyContinue
cmd /c "palm-package.bat `"$stage`" `"$root\package`" 2>&1" | Select-Object -Last 1
$ipk = Get-ChildItem "com.palm.stark.gmessages_*_all.ipk" | Sort-Object LastWriteTime | Select-Object -Last 1
if (-not $ipk) { Pop-Location; Write-Output "RESULT: FAILED (palm-package produced no .ipk)"; exit 1 }

# Add the webOS package scripts (pmPostInstall/pmPreRemove) beside control.tar.gz
# in the .ipk (an ar archive). palm-package cannot put them there, so repack with
# Windows' own tar (bsdtar), exactly as the Jellyfin project does.
$tar = Join-Path $env:SystemRoot "System32\tar.exe"
$work = Join-Path $buildDir "repack"
if (Test-Path $work) { Remove-Item -Recurse -Force $work }
New-Item -ItemType Directory -Force $work | Out-Null
& $tar -xf $ipk.FullName -C $work
Copy-Item (Join-Path $root "scripts\*") $work
$scripts = @(Get-ChildItem (Join-Path $root "scripts") | ForEach-Object { $_.Name })
& $tar -cf $ipk.FullName --format=ar --uid 0 --gid 0 -C $work debian-binary control.tar.gz data.tar.gz @scripts
Remove-Item -Recurse -Force $work
Remove-Item -Recurse -Force $stage

# webOS runs the scripts as programs, so they must be executable, which files from
# Windows are not: set mode 755 on each script's ar header (60 bytes: name 16,
# time 12, owner 6, group 6, mode 8, size 10, end 2).
$bytes = [IO.File]::ReadAllBytes($ipk.FullName)
$ascii = [Text.Encoding]::ASCII
$at = 8
while ($at + 60 -le $bytes.Length) {
    $name = $ascii.GetString($bytes, $at, 16)
    $size = [int]$ascii.GetString($bytes, $at + 48, 10).Trim()
    if ($name -notmatch '^(debian-binary|control\.tar\.gz|data\.tar\.gz|//)') {
        $mode = $ascii.GetBytes("100755  ")
        [Array]::Copy($mode, 0, $bytes, $at + 40, 8)
    }
    $at += 60 + $size + ($size % 2)
}
[IO.File]::WriteAllBytes($ipk.FullName, $bytes)
Pop-Location
Write-Output "RESULT: built $($ipk.Name)"
