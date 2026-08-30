# Monutchee Provisioning Station

Provisioning Station is a local, cross-platform hardware agent for repeatable
factory and developer provisioning. The first supported operation is a Xilinx
RAM boot over JTAG, with the boot payload served by the agent's read-only TFTP
server.

The agent is open source and intentionally contains no Vivado or Xilinx
libraries. It invokes `xsdb` from an existing Vivado/Vitis installation and can
connect to either a local or remote `hw_server`.

```text
Browser / mnc / future cloud controller
                  │ HTTP API v1
                  ▼
        ┌─────────────────────┐
        │ Local Station agent │
        │ jobs + audit events │
        └──────┬────────┬─────┘
               │        │
          XSDB │        │ read-only TFTP
               ▼        ▼
        Xilinx hw_server ───► target board
```

The cloud control plane, production records, signing service, Azure IoT key
issuance, and manufacturing policy are separate private software. The public
agent exposes the stable boundary they can call later.

## Documentation

- [User guide](doc/README.md) — installation, configuration, browser operation,
  `mnc deploy`, authentication, and troubleshooting.
- [Building guide](doc/BUILDING.md) — Linux and Windows builds, tests,
  cross-compilation, Debian packages, MSI creation, and release verification.

## What is implemented

- One Go binary for Linux and Windows, with no runtime package dependencies.
- Embedded local browser UI and a versioned JSON/streaming HTTP API.
- Strict import of `mnc-station-artifact` format v2 archives.
- Content-addressed artifact storage with checksum, size, mode, ordering, and
  archive-path validation.
- Persistent, serialized hardware jobs with cancellation and audit events.
- Xilinx `xsdb` execution against `tcp:<host>:<port>` hardware-server URLs.
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

The dashboard is served at `http://127.0.0.1:8042/`. An explicit XSDB path can
also be passed with `--xsdb-path`. If neither is set, the agent checks `PATH`,
`XILINX_VITIS`, and `XILINX_VIVADO`.

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
<hw-server-url> <tftp-server-ipv4> [board-ipv4]
```

Use `mnc-station inspect <artifact.tar.gz>` to validate and summarize an
artifact without retaining it.

## Configuration

The most useful service flags and environment variables are:

| Flag | Environment | Default |
| --- | --- | --- |
| `--http-listen` | `MNC_STATION_HTTP_LISTEN` | `127.0.0.1:8042` |
| `--tftp-listen` | `MNC_STATION_TFTP_LISTEN` | `:69` |
| `--data-dir` | `MNC_STATION_DATA_DIR` | platform user config directory |
| `--xsdb-path` | `MNC_XSDB` | auto-detect |
| `--api-token-file` | `MNC_STATION_TOKEN_FILE` | none |

`MNC_STATION_TOKEN` can supply the token directly. The agent refuses to bind
the HTTP API to a non-loopback address unless a token of at least 16 characters
is configured. Put TLS or a mutually authenticated reverse proxy in front of
the agent before exposing it beyond a trusted station network.

## HTTP API

The API is rooted at `/api/v1`. Its OpenAPI description is
[`api/openapi.yaml`](api/openapi.yaml). The important flow is:

1. `POST /api/v1/artifacts` with one multipart `artifact` file.
2. `POST /api/v1/jobs` with the artifact ID and connection settings.
3. Poll `GET /api/v1/jobs/{id}` and `GET /api/v1/jobs/{id}/events`, or request
   `text/event-stream` for events.
4. `POST /api/v1/jobs/{id}/cancel` when cancellation is needed.

`GET /api/v1/health` is intentionally public. All other API endpoints require
`Authorization: Bearer <token>` when authentication is configured.

## Distribution and Windows testing

`make cross` produces Linux amd64/arm64 and Windows amd64 binaries. GitHub
Actions also runs the full test suite natively on both Ubuntu and Windows, so
Windows compilation and socket behavior are checked even when development is
done from Linux. Tagged releases produce tar/zip archives, a Debian package,
and a Windows MSI.

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

Licensed under Apache-2.0.
