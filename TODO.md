# To do (gmessages-webos) — updated 2026-10-18
Done this round: single-ipk install (tools\build-ipk.ps1), on-device Atlas pairing + page, emoji PNG, received images/videos show in Messaging.
Tested on the tablet: reboot, live arrival, replying. Untested: group chats.
Fixed 2026-10-18 (needs a test): a live picture the phone was still fetching was stored as "[Picture]" and never downloaded; the later update with the media now fills it in.
Still open: sending pictures from the tablet; on-demand full-size image (GetFullSizeImage) for backfill; video plays only if the TouchPad decodes the format; read-state sync; group chats; the session file sits where any root program can read it.
