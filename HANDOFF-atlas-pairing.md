# Task for OpenCode: pair from the Atlas browser, on the TouchPad alone

Implement one new way to pair this relay with a phone: read the Google sign-in that
the **Atlas** browser already holds on the TouchPad, and pair with it. No PC, no second
device, no QR code.

Work in `H:\gmessages-webos`. When done, stop — do not install anything on the device.
The human reviews the code first.

## Background you need

`gmessages-webos` is a Go program (`relay/`) that runs on an HP TouchPad (webOS). It pairs
with Google Messages the way "Messages for web" does, and shows the phone's conversations in
the tablet's Messaging app. Pairing needs the cookies of a browser signed in to Google.

Two ways to pair already exist and work; copy their shape:

- `POST /pair/google` with `{"file": "...", "external": bool}` — reads a cookie file off the
  USB drive (`cookiesFromText` in `relay/main.go`), then calls `startGooglePairing(cookies)`.
- `POST /pair/phone` — opens a page for a phone's browser (`relay/handoff.go`).

Both end by calling `startGooglePairing(cookies map[string]string) (emoji string, err error)`,
which is already written in `relay/main.go`. **Do not change the pairing internals.** Your job
is only to get the cookies from Atlas and feed that same function.

## What Atlas gives you

- Atlas is `org.webosports.app.atlas`. It is a current web engine and *can* sign in to Google
  on the TouchPad. It keeps its cookies in a SQLite database at:
  `/var/atlas252/webkit-data/cookies.db`
  (a stable symlink into the app's data dir).
- Schema: `moz_cookies(id, name, value, host, path, expiry, lastAccessed, isSecure, isHttpOnly, sameSite)`.
- The cookies pairing needs live on host `.google.com`: **SID, HSID, SSID, APISID, SAPISID**.
  Confirmed present once the user signs in to Google in Atlas.
- **OSID is not required** and will not be there (Atlas cannot open messages.google.com, the only
  page that sets it). `startGooglePairing` and `cookiesFromText` were already changed to require
  only the five names above; match that — include OSID and `__Secure-1PSIDTS` if present, never
  require them.
- `/usr/bin/sqlite3` exists on the device. The relay runs as root and may read the database.

## Build the reading step

Add `relay/browser.go` with a function that returns the Atlas Google cookies as
`map[string]string` (or an error with words a person can read):

- Read the cookie values from `cookies.db`. The relay shells out to other device tools the same
  way already — see the `luna()` function in `relay/webos.go` for the `exec.CommandContext`
  pattern (capture stdout, a timeout, trim). Use `/usr/bin/sqlite3` the same way. Suggested query:
  `SELECT name || char(9) || value FROM moz_cookies WHERE host LIKE '%google.com' AND name IN ('SID','HSID','SSID','APISID','SAPISID','OSID','__Secure-1PSIDTS');`
  Split each line on the tab. (Values can contain `=`, `;` and `/`, so do not parse them as a
  cookie header — read the two columns separately, which the tab split gives you.)
- Atlas may be running, so the database can be locked (WAL). Open it read-only and tolerant: pass
  the path as a URI, `file:<path>?immutable=1`, so a running Atlas does not block the read. If that
  still fails, copy `cookies.db` (and `cookies.db-wal`, `cookies.db-shm` if present) to a temp path
  under `/tmp`, read the copy, and delete it.
- If the five required cookies are not all found, return an error like
  `"sign in to your Google account in the Atlas browser first"`. Do not log cookie values.

## Wire it up

In `relay/main.go`:

- Add `POST /pair/atlas` (register in the mux next to the other `/pair/*` routes). It takes an
  optional `?external=1` query like the others. It must:
  1. read the Atlas cookies with your new function;
  2. on error, `writeError(w, http.StatusConflict, err)`;
  3. else `r.setExternal(req.URL.Query().Get("external") != "")`, then
     `emoji, err := r.startGooglePairing(cookies)`;
  4. on success, answer like `/pair/google` does:
     `writeJSON(w, http.StatusOK, map[string]any{"pairing": true, "emoji": emoji, "emojiImage": libgm.GetEmojiSVG(emoji)})`.
- It is a POST, so the existing `guard` already requires the `X-GMRelay` header; nothing to add.

In the tablet page `app/source/App.js` (Enyo 1, ES5 only — no `let`, no arrow functions, no
`const`; indent with tabs; log with `GM.log`, never `console`):

- Add a button **above** the existing "With your phone's browser" group, in its own
  `RowGroup` captioned "On this TouchPad (Atlas)". Help text: "Sign in to Google in the Atlas
  browser, then tap this. Nothing leaves the tablet." Button caption "Pair from Atlas", `onclick: "atlasClick"`.
- Add the handler, modelled exactly on `phoneClick`:
  `atlasClick: function() { var self = this; this.showError(""); this.$.status.setContent("Reading the Atlas sign-in…"); GM.Relay.post("/pair/atlas" + (this.hosted ? "?external=1" : ""), {}, function(error) { self.began(error); }); }`
- The existing status loop already shows the emoji-to-tap once pairing starts; you do not need a
  new view.

## Build and check (do NOT install)

Portable Go is at `G:\Go` (not on PATH). From `H:\gmessages-webos\relay`:

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

Both must pass clean. Then from `H:\gmessages-webos`: `G:\NodeJS\node.exe tools\check.js`
(checks the Enyo app against the device's old engine) — must pass.

Do not run `tools\install.ps1`, do not touch the device, do not commit.

## Done when

- `relay/browser.go` reads the five Google cookies from Atlas and errors clearly when the user
  has not signed in.
- `POST /pair/atlas` pairs using them, answering like `/pair/google`.
- The tablet page has a "Pair from Atlas" button that starts it.
- `go vet`, the cross-build, and `tools\check.js` all pass.
- No existing endpoint or the pairing internals changed; cookie values are never logged.
