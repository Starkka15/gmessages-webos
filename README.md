# Google Messages for webOS

Your phone's Google Messages conversations, SMS and RCS alike, in the HP TouchPad's own
Messaging app: history, new messages as they arrive, and replies typed on the tablet.

It runs entirely on the TouchPad. A small relay (`relay/`, written in Go) pairs with the
Google Messages app on an Android phone the way "Messages for web" does, keeps that connection
up, and writes the conversations into webOS's message database as a Messaging account.

## What it needs

- webOS Community Edition 3.1.0 on the TouchPad (its Messaging app accepts outside transports).
- An Android phone with Google Messages, online. The phone does the actual sending.

## Parts

- `relay/` — the relay. `main.go` is the connection and a local HTTP interface
  (`127.0.0.1:7340`); `webos.go` is the Messaging side; `listen_linux.go` works around a
  system call the TouchPad's 2.6.35 kernel lacks.
- `third_party/mautrix-gmessages/` — the protocol library (libgm), with one fix for 32-bit
  processors; see its `PATCHES.md`.
- `webos/account/` — the account description Messaging reads.
- `webos/com.palm.stark.gmrelay` — the job that starts the relay when the tablet boots.
- `tools/` — building aside, everything is done from these with the TouchPad on USB.

## Building and installing

Go 1.26 or later:

```
cd relay
set CGO_ENABLED=0& set GOOS=linux& set GOARCH=arm& set GOARM=7
go build -trimpath -ldflags "-s -w" -o build/gmrelay .
```

then `tools\install.ps1`.

## Pairing

- Where the phone offers a QR code: `tools\pair-from-pc.ps1` shows the code on the PC.
- Where it does not (the United States, at the time of writing): `tools\pair-google.ps1
  <file>`, with a file holding the cookies of a browser signed in to the phone's Google
  account. The script says how to get them. Those cookies are a full sign-in to that account
  and are kept on the tablet; treat the tablet accordingly.

## License

AGPL-3.0, as the protocol library is.
