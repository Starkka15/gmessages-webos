# Task for OpenCode: one-app install, and show received images/videos

Two independent pieces of work on `H:\gmessages-webos`. Build and check only — do NOT install
on the device or commit. The human installs and tests, and reviews the code first.

Orientation: this is a Go relay (`relay/`) plus an Enyo 1 app (`app/`) for an HP TouchPad
(webOS CE 3.1.0). It shows a phone's Google Messages in the stock Messaging app. It is already
working; these two tasks make it ship as one package and show media. Read `README.md` and
`gmessages-webos-relay` facts first. Portable Go is at `G:\Go`; build env and commands are at the
bottom.

---

## PART A — install everything as a single app

Right now `tools\install.ps1` stages four things separately over USB:

1. the relay binary → `/media/cryptofs/apps/usr/palm/gmrelay/gmrelay`
2. the account template → `/media/cryptofs/apps/usr/palm/accounts/com.palm.stark.gmessages/` (+ `images/`)
3. the boot job → `/var/palm/event.d/com.palm.stark.gmrelay`
4. the Enyo app `.ipk` → `com.palm.stark.gmessages`

(The database kinds and permissions are made by the relay itself at first run — leave that as is.)

**Goal:** one `.ipk` that, when installed with `palm-install`, puts all of 1–4 in place, starts
the relay, and makes the account type appear — and that removes them all when uninstalled.

### How (hand-assembled ipk, the reliable webOS CE way)

`palm-package` only lays down the app tree and will not place the account template, the boot job,
or run install scripts. Build the `.ipk` by hand instead (it is an `ar` archive). Add
`tools\build-ipk.ps1` that produces `build\com.palm.stark.gmessages_<version>_all.ipk` containing:

- `debian-binary` — the line `2.0`.
- `data.tar.gz` — the full file tree at the paths above:
  - `./media/cryptofs/apps/usr/palm/applications/com.palm.stark.gmessages/` — the Enyo app
    (everything in `app/`, with `appinfo.json` at its root).
  - `./media/cryptofs/apps/usr/palm/gmrelay/gmrelay` — the relay binary (mode 0755).
  - `./media/cryptofs/apps/usr/palm/accounts/com.palm.stark.gmessages/` — the account template
    JSON and `images/` (from `webos/account/`).
- `control.tar.gz` — `control`, `postinst`, `postrm` (postinst/postrm mode 0755):
  - **control**: Package `com.palm.stark.gmessages`, Version `<from app/appinfo.json>`,
    Architecture `all`, plus the usual Section/Priority/Maintainer/Description lines.
  - **postinst**: copy `webos/com.palm.stark.gmrelay` (ship it inside the app dir and copy from
    there) to `/var/palm/event.d/com.palm.stark.gmrelay`; `mkdir -p` the relay's `state` dir;
    tell the accounts service to reload: `luna-send -n 1 palm://com.palm.service.accounts/__quit '{}'`;
    `start com.palm.stark.gmrelay` (ignore "already running"); `exit 0`.
  - **postrm**: `curl -s -m 20 -X POST -H "X-GMRelay: 1" http://127.0.0.1:7340/unpair` (so the
    account and session go); `stop com.palm.stark.gmrelay 2>/dev/null`; `killall gmrelay 2>/dev/null`;
    `rm -f /var/palm/event.d/com.palm.stark.gmrelay`; `rm -rf /media/cryptofs/apps/usr/palm/gmrelay`;
    `exit 0`.

  Check the exact ipk layout against the webos-mcp `packaging` topic (and `gotchas`) — follow what
  it says over this sketch if they differ. `ar`, `tar` and `gzip` for the build: use the ones in
  WSL or Git for Windows; keep tar entries as `./`-relative with Unix permissions and line endings.

- Keep `tools\install.ps1` working for development (it is fine as is). `build-ipk.ps1` is the
  shippable artifact; it must not need a device attached.
- The relay binary must be cross-built for the device first (linux/arm, see below) and copied into
  the package from `relay\build\gmrelay`.

### Done when

- `tools\build-ipk.ps1` makes one `.ipk` with all four pieces and the two scripts.
- `ar t` on the result lists `debian-binary`, `control.tar.gz`, `data.tar.gz`; `tar tzf` on each
  shows the layout above with the relay binary executable and the scripts present.
- Nothing is installed by you.

---

## PART B — show received images and videos

The CE Messaging app already renders media from a message's **`filePath`** field (confirmed in its
`ConversationItem.js`): an image file shows inline; a video or audio file shows as a tappable chip
that opens in the right app (the TouchPad plays H.264 mp4). The file must have a known extension:
images `jpg jpeg png gif webp bmp avif`, video `mp4 m4v mov webm ogv wmv 3gp mkv ts`,
audio `mp3 m4a aac ogg oga opus flac wav wma amr`. So the relay only has to **download the media
and set `filePath`** on the message it writes.

Work in `relay/webos.go` (and `relay/main.go`'s `plainMessage`/`Message` if you need the media id).

- The library downloads media with
  `cli.DownloadMedia(mediaID string, key []byte) (io.ReadCloser, error)`.
  A `*gmproto.MediaContent` gives `GetMediaID()`, `GetDecryptionKey()`, `GetMimeType()`,
  `GetMediaName()`, `GetSize()`. `plainMessage` already walks `GetMessageInfo()` and sees each
  `part.GetMediaContent()` — right now it only records mime/name/size into `Message.Media`. Carry
  the media id and key through too (add unexported fields to the `Media` struct, or download in the
  bridge straight from the raw `*gmproto.Message`).
- When the bridge stores a message that has media, for each media part:
  1. choose an extension from the mime type (`image/jpeg`→`jpg`, `image/png`→`png`, `image/gif`→`gif`,
     `image/webp`→`webp`, `video/mp4`→`mp4`, `video/3gpp`→`3gp`, `audio/mpeg`→`mp3`,
     `audio/ogg`→`ogg`, `audio/aac`→`aac`, `audio/amr`→`amr`; fall back to the extension of
     `GetMediaName()`, else `bin`);
  2. download the bytes with `DownloadMedia`, and write them to
     `/media/internal/Google Messages/<conversation id>/<message id>.<ext>`
     (`mkdir -p` the folder; if the file already exists with non-zero size, skip the download);
  3. set `filePath` on the db message record to that absolute path (no `file://` prefix — the
     Messaging app adds it). Keep the caption in `messageText` as now. Drop the `"[Picture]"` /
     `"[Video]"` placeholder text when a real file is attached (a message can have both a caption
     and media — keep the caption, lose only the placeholder).
- A message with several media parts: `filePath` holds one file, so use the first real
  (non-thumbnail) part; it is fine to leave the rest for later.
- **Do not block or flood on history backfill.** Live incoming messages download their media right
  away. For the initial history sync (up to ~290 messages) do not download everything inline:
  either download media only for messages newer than a day, or queue the downloads a few at a time
  in the background, so the sync still finishes quickly and the phone/CDN is not hammered. Messages
  whose media is not (yet) downloaded keep the current `"[Picture]"`/`"[Video]"` text.
- Never log media bytes or decryption keys. Failures to download must not stop the message being
  stored (store it with the placeholder text and move on).
- Sending images from the tablet is **out of scope** for this task.

### Done when

- A received photo shows inline in the Messaging conversation; a received video shows as a chip
  that opens the Video app.
- Backfill of a large history still completes promptly and does not download every old attachment
  at once.
- `go vet` and the cross-build pass; no media bytes or keys are logged.

---

## Build and check (do NOT install)

From `H:\gmessages-webos\relay`:

```
set GOPATH=G:\GoPath
set GOCACHE=G:\GoPath\cache
set GOFLAGS=-mod=mod
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=arm
set GOARM=7
G:\Go\bin\go.exe vet ./...
G:\Go\bin\go.exe build -trimpath -ldflags "-s -w" -o build\gmrelay .
```

Then from `H:\gmessages-webos`: `G:\NodeJS\node.exe tools\check.js` (the Enyo app). All must pass.
Do not run `tools\install.ps1`, do not touch the device, do not commit.
