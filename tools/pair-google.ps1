# Pair the relay on the TouchPad with a phone through its Google account.
#     .\tools\pair-google.ps1 C:\path\to\cookies.txt
#     .\tools\pair-google.ps1                  (again, with the cookies the relay kept)
# The file holds what a signed-in browser sends to messages.google.com: a request
# copied from the browser's developer tools as cURL, or just its Cookie header.
# It is read here, sent to the TouchPad over the USB cable, and then deleted.
param([string]$CookieFile = "")
. (Join-Path $PSScriptRoot "tp-tools.ps1")
if ($CookieFile) {
    $text = [IO.File]::ReadAllText($CookieFile)
    $wanted = "SID", "HSID", "OSID", "SSID", "APISID", "SAPISID", "__Secure-1PSIDTS"
    $cookies = @{}
    foreach ($name in $wanted) {
        # name=value, ended by ; or a quote or the end of the line
        $m = [regex]::Match($text, "(?<![\w-])" + [regex]::Escape($name) + "=([^;'`"\s]+)")
        if ($m.Success) { $cookies[$name] = $m.Groups[1].Value }
    }
    $missing = $wanted[0..5] | Where-Object { -not $cookies.ContainsKey($_) }
    if ($missing) {
        Write-Output ("RESULT: FAILED (the file has no " + ($missing -join ", ") + " cookie)")
        exit 1
    }
    $body = @{ cookies = $cookies } | ConvertTo-Json -Compress
} else {
    $body = "{}"
}
$f = Join-Path $env:TEMP "gm-cookies.json"
[IO.File]::WriteAllText($f, $body, (New-Object Text.UTF8Encoding $false))
Send-TpFile $f "/tmp/gm-cookies.json"
Remove-Item $f
$answer = (Invoke-Tp 'curl -s -m 90 -X POST -H "X-GMRelay: 1" -H "Content-Type: application/json" --data-binary @/tmp/gm-cookies.json http://127.0.0.1:7340/pair/google; rm -f /tmp/gm-cookies.json') -join ""
# The relay keeps the cookies from here on, so the file is no longer needed.
if ($CookieFile -and $answer -notmatch 'cookie is missing|could not be read') { Remove-Item $CookieFile }
if ($answer -notmatch '"emojiImage":"([^"]+)"') {
    Write-Output "RESULT: FAILED $answer"
    exit 1
}
$page = Join-Path $env:TEMP "gm-pair.html"
@"
<!doctype html><meta charset="utf-8"><title>Pair Google Messages</title>
<body style="font-family:sans-serif;text-align:center">
<h2>Your phone is showing three pictures. Tap this one:</h2>
<img src="$($Matches[1])" width="260" height="260">
"@ | Set-Content -Encoding utf8 $page
Start-Process $page
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Seconds 4
    $state = (Invoke-Tp 'curl -s -m 5 http://127.0.0.1:7340/status') -join ""
    if ($state -notmatch '"state":"pairing"') { break }
}
Remove-Item $page -ErrorAction SilentlyContinue
Write-Output "RESULT: $state"
