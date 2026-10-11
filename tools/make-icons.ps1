# Draw the account's icons: a speech bubble, in the two sizes webOS asks for.
Add-Type -AssemblyName System.Drawing
$out = Join-Path (Split-Path $PSScriptRoot -Parent) "webos\account\images"
New-Item -ItemType Directory -Force $out | Out-Null
foreach ($size in 32, 48, 64) {
    $bmp = New-Object System.Drawing.Bitmap $size, $size
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = "AntiAlias"
    $g.Clear([System.Drawing.Color]::Transparent)
    $blue = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 26, 115, 232))
    $white = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::White)
    $s = $size / 32.0
    # the bubble: a rounded box with a tail at the bottom left
    $path = New-Object System.Drawing.Drawing2D.GraphicsPath
    $r = 7 * $s; $x = 3 * $s; $y = 4 * $s; $w = 26 * $s; $h = 19 * $s
    $path.AddArc($x, $y, $r, $r, 180, 90)
    $path.AddArc($x + $w - $r, $y, $r, $r, 270, 90)
    $path.AddArc($x + $w - $r, $y + $h - $r, $r, $r, 0, 90)
    $path.AddLine($x + $w - $r, $y + $h, $x + 11 * $s, $y + $h)
    $path.AddLine($x + 11 * $s, $y + $h, $x + 4 * $s, $y + $h + 6 * $s)
    $path.AddLine($x + 4 * $s, $y + $h + 6 * $s, $x + 5 * $s, $y + $h)
    $path.AddArc($x, $y + $h - $r, $r, $r, 90, 90)
    $path.CloseFigure()
    $g.FillPath($blue, $path)
    # three lines of "text"
    foreach ($row in 0..2) {
        $len = @(18, 18, 11)[$row]
        $g.FillRectangle($white, 7 * $s, (9 + 4.5 * $row) * $s, $len * $s, 2 * $s)
    }
    $g.Dispose()
    # 64 is the launcher icon of the pairing app.
    $file = if ($size -eq 64) { Join-Path (Split-Path $PSScriptRoot -Parent) "app\images\icon.png" } else { Join-Path $out "gmessages-${size}x${size}.png" }
    $bmp.Save($file, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}
Get-ChildItem $out | Select-Object Name, Length
