# Pair the relay on the TouchPad with a phone, showing the code to scan on this PC.
# (A stopgap until the TouchPad app shows the code itself.) The relay only listens
# on the tablet, so the code is fetched over the USB cable.
. (Join-Path $PSScriptRoot "tp-tools.ps1")
$dir = Join-Path $env:TEMP "gmrelay-pair"
New-Item -ItemType Directory -Force $dir | Out-Null
$png = Join-Path $dir "qr.png"
$page = Join-Path $dir "pair.html"
@'
<!doctype html><title>Pair Google Messages</title>
<body style="font-family:sans-serif;text-align:center;background:#fff">
<h2>Google Messages on your phone: profile picture, Device pairing, QR code scanner</h2>
<img id="q" src="qr.png" width="480" height="480">
<p>The code renews every 30 seconds. This page follows it.</p>
<script>setInterval(function(){document.getElementById("q").src="qr.png?"+Date.now()},2000)</script>
'@ | Set-Content -Encoding utf8 $page
Invoke-Tp 'curl -s -m 10 -X POST -H "X-GMRelay: 1" http://127.0.0.1:7340/pair' | Out-Null
$opened = $false
for ($i = 0; $i -lt 60; $i++) {
    $state = (Invoke-Tp 'curl -s -m 5 -o /tmp/gm-qr.png http://127.0.0.1:7340/qr.png; curl -s -m 5 http://127.0.0.1:7340/status') -join ""
    if ($state -notmatch '"state":"pairing"') { break }
    Get-TpFile "/tmp/gm-qr.png" $png
    if (-not $opened) { Start-Process $page; $opened = $true }
    Start-Sleep -Seconds 4
}
Invoke-Tp 'rm -f /tmp/gm-qr.png' | Out-Null
Remove-Item $png -ErrorAction SilentlyContinue
Write-Output "RESULT: $state"
