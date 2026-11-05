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
Browser / mps CLI, TUI, MCP / future cloud controller
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
- [Deployment tasks](doc/DEPLOYMENT_TASKS.md) — trusted task registration, lifecycle,
  cancellation, and capability negotiation.
- [Building guide](doc/BUILDING.md) — Linux and Windows builds, tests,
  cross-compilation, Debian packages, MSI creation, and release verification.

## What is implemented

- Separate `mpsd` daemon and `mps` CLI/TUI/MCP client for Linux and Windows.
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
make build
MPS_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb \
  ./dist/mpsd serve --data-dir="$PWD/.station-dev" --open-browser
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
is discoverable. It runs as the dedicated `mps` account rather than a
login user and listens only on `127.0.0.1:3121` by default. XSDB itself remains
an on-demand process started for target discovery and provisioning jobs.

Real boards request TFTP on UDP port 69. On Linux, install the deb/service (it
has only the narrow low-port capability) or grant that capability to a local
development binary:

```bash
sudo setcap cap_net_bind_service=+ep ./dist/mpsd
```

For UI/API development without a board, use an unprivileged port such as
`--tftp-listen=127.0.0.1:6969`. This does not change the board's standard TFTP
port, so it is not suitable for the real boot flow.

## Terminal and automation clients

Run `mps` without arguments for the terminal UI. It connects to the local HTTP
API and uses OS-authorized service startup without password prompts. If service
startup is denied, use an administrator terminal; the client never elevates
itself. A standalone daemon can be started with `mpsd serve --data-dir=...`.
Use `mps help` for the CLI surface, including status, target discovery, artifacts,
jobs, serial discovery/recorded output, and service control. A live terminal
console is deferred.

For scripts, pass global connection flags before the command:

```bash
mps --url http://127.0.0.1:8042 status
mps artifact import /path/to/artifact.tar.gz
mps target list
mps job submit --request /path/to/job.json --acknowledge-reset --request-id 0123456789abcdef0123456789abcdef
mps job reconcile 0123456789abcdef0123456789abcdef
mps job logs --follow JOB_ID
mps storage status
mps storage cleanup --dry-run
mps storage cleanup --apply
```

A submission must name an explicit target. Hardware reset acknowledgement is
required. Request IDs are 32 lowercase hexadecimal characters; the client
generates and prints one before submitting when omitted. Reconcile uncertain
submissions with that same request ID.

The station retains the latest 30 completed job histories plus active jobs. Its
10 GiB active state budget counts logical file bytes, including import staging,
and reserves space for active job output and terminal metadata. Pre-existing
protected or unknown state can exceed the budget; the station reports this and
rejects further growth until space is available. `mps storage status` reports
used, reserved, available, and unknown bytes plus external recovery locations.
Automatic cleanup runs at startup, job completion, and before growth. Manual
cleanup requires an explicit `--dry-run` preview or `--apply` deletion. Historical jobs can remain visible
after their artifact payload is reclaimed; reimport the original archive to
provision again. Truncated event history is marked in job inspection. Legacy
installations should follow [the cleanup and migration guide](doc/MIGRATION.md).

`mps mcp` serves MCP over stdio with the same explicit workflow. It never
starts a service automatically. Client disconnects do not cancel jobs; cancel
explicitly. The browser, terminal, CLI, and MCP all use `/api/v1`, and only the
daemon owns hardware and persistent state.

## Artifact workflow

demo board Yocto builds publish `demo-board-jtag-image.tar.gz` in
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

Use `mpsd inspect <artifact.tar.gz>` to validate and summarize an
artifact without retaining it.

## Configuration

The most useful service flags and environment variables are:

| Flag | Environment | Default |
| --- | --- | --- |
| `--http-listen` | `MPS_HTTP_LISTEN` | `0.0.0.0:8042` |
| `--tftp-listen` | `MPS_TFTP_LISTEN` | `:69` |
| `--data-dir` | `MPS_DATA_DIR` | `/var/lib/mps` on Linux; ProgramData on Windows |
| `--xsdb-path` | `MPS_XSDB` | auto-detect |
| `--api-token-file` | `MPS_TOKEN_FILE` | none |
| `--serial-baud` | `MPS_SERIAL_BAUD` | `115200` |
| `--max-console-log-bytes` | `MPS_MAX_CONSOLE_LOG_BYTES` | `16777216` |

The companion `mpsd hw-server` command accepts `--hw-server-path` and
`--listen`. Their service environment equivalents are `MPS_HW_SERVER` and
`MPS_HW_SERVER_LISTEN`; the defaults are automatic executable discovery and
`tcp:127.0.0.1:3121`.

`MPS_TOKEN` can supply the token directly. Direct browser/API requests
to an IP loopback URL such as `http://127.0.0.1:8042` or `http://[::1]:8042`
do not require the token. A LAN URL—including any `172.x.x.x` address—still
requires it. When listening on a non-loopback address without an explicitly
configured token, the agent creates
a private `api-token` file in its data directory. The Debian service stores it
at `/var/lib/mps/api-token`; display it with
`sudo mpsd token --service` and enter it when the dashboard asks.
Rotate a managed Debian service token after accidental disclosure with:

```bash
sudo mpsd token --rotate --service
sudo systemctl restart mps
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

The Windows MSI installs both programs and the native `mps` service under
LocalService, with private state in `%ProgramData%\Monutchee\MPS`. The Start
menu opens `mps`; `mpsd.exe serve` remains available for foreground debugging.
Linux packages install `mps.service` and optional `mps-hw-server.service` under
the `mps` account. See [migration](doc/MIGRATION.md) for legacy installations.

## Development

```bash
make check       # formatting, vet, and tests
make test-race   # race detector on supported host platforms
make cross       # Linux and Windows release-shaped binaries
```

Hardware tests are deliberately separate from unit tests. A successful demo board
smoke test requires XSDB to finish, every file declared below the artifact's
`tftpRoot` to be requested successfully, and Linux to boot on the board.

Licensed under Apache-2.0. Bundled dependency licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
