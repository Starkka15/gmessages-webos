# Helpers for working on the TouchPad over novacom. Dot-source this file:
#     . .\tools\tp-tools.ps1
$script:Nov = "C:\Program Files\Palm, Inc\novacom.exe"

# Run a shell script on the TouchPad. Going through a file avoids PowerShell's quoting problems.
function Invoke-Tp([string]$Script) {
    $f = Join-Path $env:TEMP "tp-cmd.sh"
    [IO.File]::WriteAllText($f, ($Script -replace "`r`n", "`n") + "`n", (New-Object Text.UTF8Encoding $false))
    cmd /c "`"$script:Nov`" -d usb put file:///tmp/tp-cmd.sh < `"$f`"" | Out-Null
    & $script:Nov -d usb run file:///bin/sh /tmp/tp-cmd.sh 2>&1
}

# Copy a file from the TouchPad to this PC, or from this PC to the TouchPad.
function Get-TpFile([string]$Remote, [string]$Local) {
    cmd /c "`"$script:Nov`" -d usb get file://$Remote > `"$Local`""
}
function Send-TpFile([string]$Local, [string]$Remote) {
    cmd /c "`"$script:Nov`" -d usb put file://$Remote < `"$Local`""
}

# Take a screenshot of the TouchPad. The image comes out in the panel's own orientation.
function Get-TpShot([string]$Out) {
    Invoke-Tp "luna-send -n 1 palm://com.palm.systemmanager/takeScreenShot '{`"file`":`"/media/internal/.gm-shot.png`"}' >/dev/null 2>&1" | Out-Null
    Get-TpFile "/media/internal/.gm-shot.png" $Out
}

