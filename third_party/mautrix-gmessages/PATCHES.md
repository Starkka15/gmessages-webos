# Changes from upstream

This is mautrix-gmessages (https://github.com/mautrix/gmessages, AGPL-3.0) at
commit 186284eea622, with the bridge's command, Docker and CI files removed and
one change:

- `pkg/libgm/pair_google.go`, `ProcessServerInit`: the pairing emoji is picked
  with unsigned arithmetic. Upstream converts a 32-bit hash to `int` before the
  modulo, which goes negative on 32-bit platforms (the HP TouchPad is ARMv7) and
  panics with "index out of range".
