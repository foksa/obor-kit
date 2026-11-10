# Upgrading an obor project from v0.33 to v0.34

## v0.34.0 (config shape 1 is refused)

v0.33 read a shape-1 `.obor/config.json` (no `version: 2`) for one release and rewrote it as shape 2 at serve's first
start. v0.34 no longer reads shape 1: every command, hook and `obor serve` fails on such a file with one error naming
it and the way out, and `obor config migrate` is gone.

**Before upgrading,** check that each project's config says `"version": 2`:

```sh
grep -q '"version": *2' .obor/config.json && echo "shape 2: ready for v0.34" || echo "shape 1: migrate with obor v0.33 first"
```

A project still on shape 1 (one whose serve never started on v0.33): install v0.33, run `obor config migrate` in the
project (or start its serve once), commit, then upgrade. The v0.33 guide in the `v0.33.0` tag's tree lists every old
and new key for a rewrite by hand.

`.obor/local.json` has no version: a shape-1 key left in it is now an unknown key like any other — one warning per key
for every reader, and `obor config validate` refuses it. Rename it by the v0.33 tag's table.
