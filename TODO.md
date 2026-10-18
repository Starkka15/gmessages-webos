# To do (gmessages-webos) — updated 2026-10-18
Done this round: single-ipk install (tools\build-ipk.ps1), on-device Atlas pairing + page, emoji PNG, received images/videos show in Messaging.
Tested on the tablet: reboot, live arrival, replying, sending a picture (paperclip), receiving a video (H.264 Main 720p30 + AAC, with iPhone mebx tracks: plays). Untested: group chats.
Fixed 2026-10-18: pictures the phone was still fetching now fill in; every attachment in the synced history (last 25 per conversation) is downloaded, not just the last day. Thumbnail-only pictures ask the phone for the full size (GetFullSizeImage) and switch to it; downloads for stored messages run on their own worker so sending never waits on them.
Still open: read-state sync; group chats; the session file sits where any root program can read it.
