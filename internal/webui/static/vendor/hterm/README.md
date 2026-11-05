# Embedded hterm assets

The browser bundle contains official Chromium hterm 1.92.1 and libdot 9.0.0,
with punycode 2.3.1 and upstream wcwidth. The exact source revision and bundle
SHA-256 are recorded in `provenance.json`; the source is the pinned
`third_party/libapps` Git submodule. Upstream notices are retained in the bundle
and the adjacent license files. `messages.en.json` contains the HTERM-prefixed
English messages from the same upstream revision.

These checked-in files are embedded in the Go binary. Ordinary builds and
installation require neither Node.js nor a source submodule checkout. There
are no runtime CDN requests. See `tools/hterm/README.md` for regeneration and
browser verification. Do not edit generated files directly.
