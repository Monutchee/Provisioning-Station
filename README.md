# Monutchee Provisioning Station

Provisioning Station is a local, cross-platform hardware agent for repeatable
factory and developer provisioning. The first supported operation is a Xilinx
RAM boot over JTAG, with the boot payload served by the agent's read-only TFTP
server.

The agent is open source and intentionally contains no Vivado or Xilinx
libraries. It invokes `xsdb` from an existing Vivado, Vitis, or standalone
Hardware Server installation and can connect to either a local or remote
`hw_server`.

```text
Browser / mnc / future cloud controller
                  │ HTTP API v1
                  ▼
        ┌────────────────────────────┐
        │ Local Station agent        │
        │ jobs + audit + serial logs │
        └──────┬─────────┬───────────┘
               │         │           │
          XSDB │    TFTP │      UART │
               ▼         ▼           ▼
        Xilinx hw_server ───► target board
```

The cloud control plane, production records, signing service, Azure IoT key
issuance, and manufacturing policy are separate private software. The public
agent exposes the stable boundary they can call later.

## Documentation

- [User guide](doc/README.md) — installation, configuration, browser operation,
  `mnc deploy`, authentication, and troubleshooting.
- [Serial console API](doc/SERIAL_CONSOLE.md) — serial discovery, FTDI identity,
  manual tty/COM selection, live WebSocket sessions, and per-job capture.
- [Building guide](doc/BUILDING.md) — Linux and Windows builds, tests,
  cross-compilation, Debian packages, MSI creation, and release verification.

## What is implemented

- One Go binary for Linux and Windows, with no runtime package dependencies.
- Embedded local browser UI and a versioned JSON/streaming HTTP API.
- Strict import of `mnc-station-artifact` format v2 archives.
- Content-addressed artifact storage with checksum, size, mode, ordering, and
  archive-path validation.
- Persistent, serialized hardware jobs with cancellation and audit events.
- Xilinx target discovery and `xsdb` execution against
  `tcp:<host>:<port>` hardware-server URLs, including queued multi-board boots.
- Stable pairing of each FT2232H JTAG cable with its channel B Linux tty or
  Windows COM port, with an explicit per-target manual-port fallback. JTAG
  remains usable without UART; optional consoles provide ANSI terminals and
  bounded RX-only job logs.
- A per-job, read-only TFTP server with RFC 1350 reads and `blksize`, `timeout`,
  and `tsize` option negotiation.
- Linux service/deb packaging, Windows MSI authoring, release archives, and
  native Windows/Linux CI.

Only `xilinx` + `jtag-boot` + `xilinx-xsdb` artifacts are executable today.
The internal boundaries allow additional executors such as NXP UUU or WIC
flashing without weakening the v2 artifact contract.

## Build and run

Go 1.27 or newer is required to build the agent.

```bash
go test ./...
go build -trimpath -o mnc-station ./cmd/mnc-station
MNC_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb \
  ./mnc-station serve --open-browser
```

The agent listens on `0.0.0.0:8042` by default. Open the dashboard locally at
`http://127.0.0.1:8042/`, or remotely at
`http://<station-ip-or-hostname>:8042/`. Use HTTP, not HTTPS, unless a TLS
reverse proxy has been configured. An explicit XSDB path can also be passed
with `--xsdb-path`. If neither is set, the agent checks `PATH` first, then
`XILINX_VITIS`/`XILINX_VIVADO`, and finally searches versioned Vivado, Vitis,
and standalone Hardware Server (`HWSRVR`) installations below `/opt/Xilinx`
on Linux or `C:\Xilinx` on Windows. This includes paths such as
`/opt/Xilinx/2025.2/HWSRVR/bin/xsdb`.

The Debian service also starts a companion local Xilinx `hw_server` when one
is discoverable. It runs as the dedicated `mnc-station` account rather than a
login user and listens only on `127.0.0.1:3121` by default. XSDB itself remains
an on-demand process started for target discovery and provisioning jobs.

Real boards request TFTP on UDP port 69. On Linux, install the deb/service (it
has only the narrow low-port capability) or grant that capability to a local
development binary:

```bash
sudo setcap cap_net_bind_service=+ep ./mnc-station
```

For UI/API development without a board, use an unprivileged port such as
`--tftp-listen=127.0.0.1:6969`. This does not change the board's standard TFTP
port, so it is not suitable for the real boot flow.

## Artifact workflow

MSAP1 Yocto builds publish `msap1-jtag-image.tar.gz` in
`build/export/provision-image/`. Import that file in the dashboard or use the
workspace integration:

```bash
./mnc deploy
```

The deploy command uploads the artifact to the local agent, queues the job,
and follows its events. It no longer needs to run XSDB itself. The Station
loader receives:

```text
<hw-server-url> <tftp-server-ipv4> [board-ipv4] [xsdb-target-id]
```

Use `mnc-station inspect <artifact.tar.gz>` to validate and summarize an
artifact without retaining it.

## Configuration

The most useful service flags and environment variables are:

| Flag | Environment | Default |
| --- | --- | --- |
| `--http-listen` | `MNC_STATION_HTTP_LISTEN` | `0.0.0.0:8042` |
| `--tftp-listen` | `MNC_STATION_TFTP_LISTEN` | `:69` |
| `--data-dir` | `MNC_STATION_DATA_DIR` | platform user config directory |
| `--xsdb-path` | `MNC_XSDB` | auto-detect |
| `--api-token-file` | `MNC_STATION_TOKEN_FILE` | none |
| `--serial-baud` | `MNC_STATION_SERIAL_BAUD` | `115200` |
| `--max-console-log-bytes` | `MNC_STATION_MAX_CONSOLE_LOG_BYTES` | `16777216` |

The companion `mnc-station hw-server` command accepts `--hw-server-path` and
`--listen`. Their service environment equivalents are `MNC_HW_SERVER` and
`MNC_HW_SERVER_LISTEN`; the defaults are automatic executable discovery and
`tcp:127.0.0.1:3121`.

`MNC_STATION_TOKEN` can supply the token directly. Direct browser/API requests
to an IP loopback URL such as `http://127.0.0.1:8042` or `http://[::1]:8042`
do not require the token. A LAN URL—including any `172.x.x.x` address—still
requires it. When listening on a non-loopback address without an explicitly
configured token, the agent creates
a private `api-token` file in its data directory. The Debian service stores it
at `/var/lib/mnc-station/api-token`; display it with
`sudo mnc-station token --service` and enter it when the dashboard asks.
Rotate a managed Debian service token after accidental disclosure with:

```bash
sudo mnc-station token --rotate --service
sudo systemctl restart mnc-station
```

The first command prints the new token. Update every client and preset before
restarting; the running service keeps the old token in memory until restart.
Put TLS or a mutually authenticated reverse proxy in front of the agent before
exposing it beyond a trusted station network.

## HTTP API

The API is rooted at `/api/v1`. Its OpenAPI description is
[`api/openapi.yaml`](api/openapi.yaml). The important flow is:

1. `POST /api/v1/artifacts` with one multipart `artifact` file.
2. `GET /api/v1/xilinx/targets?hwServerUrl=...` to discover PSU targets.
3. Use the `serialPort` association returned for each target, or call
   `GET /api/v1/serial/ports` to select a tty/COM port manually. Live consoles
   use a short-lived session from `POST /api/v1/serial/sessions` and its
   WebSocket path.
4. `POST /api/v1/jobs` with the artifact ID, connection settings,
   `targetCableSerial`, `targetDeviceIndex`, and the diagnostic `targetId`.
   Include the matched `serialConsole` to start RX capture before XSDB, use
   `selection: "manual"` for an explicit override, or omit it for JTAG-only
   operation. Submit one job per target for a multi-board boot. The stable cable
   identity is resolved inside the boot XSDB process because numeric IDs are
   session-local.
5. Poll `GET /api/v1/jobs/{id}` and `GET /api/v1/jobs/{id}/events`, or request
   `text/event-stream` for events.
6. Download captured bytes from
   `GET /api/v1/jobs/{id}/serial-transcript`, or cancel with
   `POST /api/v1/jobs/{id}/cancel`.

`GET /api/v1/health` is intentionally public. Other endpoints require
`Authorization: Bearer <token>` when authentication is configured, except for
direct IP-loopback requests. Hostnames and LAN addresses do not receive the
loopback exception.

## Distribution and Windows testing

`make cross` produces Linux amd64/arm64 and Windows amd64 binaries. GitHub
Actions also runs the full test suite natively on both Ubuntu and Windows, so
Windows compilation and socket behavior are checked even when development is
done from Linux. Tagged releases produce tar/zip archives, a Debian package,
and a Windows MSI. Pushing an annotated `vMAJOR.MINOR.PATCH` tag creates a
GitHub release and attaches every installer plus `SHA256SUMS`; see the
[building guide](doc/BUILDING.md#continuous-integration-and-tagged-releases).

The Windows installer places `mnc-station.exe` in Program Files. Run
`mnc-station.exe serve --open-browser` from a Xilinx-enabled terminal, or set
`MNC_XSDB` to the installed `xsdb.bat` path.

## Development

```bash
make check       # formatting, vet, and tests
make test-race   # race detector on supported host platforms
make cross       # Linux and Windows release-shaped binaries
```

Hardware tests are deliberately separate from unit tests. A successful MSAP1
smoke test requires XSDB to finish, every file declared below the artifact's
`tftpRoot` to be requested successfully, and Linux to boot on the board.

Licensed under Apache-2.0. Bundled dependency licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
