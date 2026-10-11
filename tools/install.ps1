# Put everything on the TouchPad attached by USB and (re)start the relay: the
# relay, its Messaging account description, its startup job, and the pairing
# app. The session on the tablet (the pairing) is left alone.
#     .\tools\install.ps1            everything
#     .\tools\install.ps1 -Relay     only the relay program
param([switch]$Relay)
. (Join-Path $PSScriptRoot "tp-tools.ps1")
$root = Split-Path $PSScriptRoot -Parent
$bin = Join-Path $root "relay\build\gmrelay"
if (-not (Test-Path $bin)) { Write-Output "RESULT: FAILED (build the relay first)"; exit 1 }
$D = "/media/cryptofs/apps/usr/palm/gmrelay"
$A = "/media/cryptofs/apps/usr/palm/accounts/com.palm.stark.gmessages"
Invoke-Tp "stop com.palm.stark.gmrelay 2>/dev/null; killall gmrelay 2>/dev/null; mkdir -p $D/state $A/images; sleep 1" | Out-Null
Send-TpFile $bin "$D/gmrelay.new"
if (-not $Relay) {
    Send-TpFile (Join-Path $root "webos\account\com.palm.stark.gmessages.json") "$A/com.palm.stark.gmessages.json"
    Get-ChildItem (Join-Path $root "webos\account\images\*.png") | ForEach-Object { Send-TpFile $_.FullName "$A/images/$($_.Name)" }
    Send-TpFile (Join-Path $root "webos\com.palm.stark.gmrelay") "/var/palm/event.d/com.palm.stark.gmrelay"
    # The pairing app, as a package.
    $env:JAVA_HOME = "G:\Java\jdk17"
    $env:Path = "G:\Java\jdk17\bin;G:\webOS\PalmSDK\Current\bin;" + $env:Path
    $build = Join-Path $root "build"
    New-Item -ItemType Directory -Force $build | Out-Null
    Push-Location $build
    Remove-Item "com.palm.stark.gmessages_*_all.ipk" -ErrorAction SilentlyContinue
    cmd /c "palm-package.bat `"$root\app`" `"$root\package`" 2>&1" | Select-Object -Last 1
    $ipk = Get-ChildItem "com.palm.stark.gmessages_*_all.ipk" | Select-Object -Last 1
    if (-not $ipk) { Pop-Location; Write-Output "RESULT: FAILED (the app was not packaged)"; exit 1 }
    cmd /c "palm-launch.bat -d usb -c com.palm.stark.gmessages 2>&1" | Out-Null
    cmd /c "palm-install.bat -d usb $($ipk.Name) 2>&1" | Select-Object -Last 1
    Pop-Location
}
Invoke-Tp @"
mv -f $D/gmrelay.new $D/gmrelay && chmod 755 $D/gmrelay
# The account service reads account descriptions when it starts.
luna-send -n 1 palm://com.palm.service.accounts/__quit '{}' </dev/null >/dev/null 2>&1
sync
start com.palm.stark.gmrelay 2>&1 | tail -1
sleep 6
curl -s -m 5 http://127.0.0.1:7340/status; echo
"@
