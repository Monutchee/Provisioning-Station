# Provisioning Station user guide

This guide explains how to run the Monutchee Provisioning Station, import a
Station artifact, and boot an demo board board through Xilinx JTAG. For compiler and
packaging instructions, see [BUILDING.md](BUILDING.md).

The first supported operation is a RAM boot using Xilinx XSDB. The Station does
not include Vivado, Vitis, XSDB, or `hw_server`; those tools remain part of the
Xilinx installation.

## How the Station fits together

```text
Browser or mnc deploy
        |
        | HTTP API (default 0.0.0.0:8042)
        v
Provisioning Station
   | XSDB             | read-only TFTP       | optional tty/COM UART
   v                  v                      v
Xilinx hw_server ── JTAG ──> target board <──┘
```

XSDB runs on the Station computer. The Xilinx `hw_server` can run on that same
computer or on another reachable computer. During boot, the target board must
be able to reach the Station computer's TFTP IPv4 address.

## Prerequisites

Before starting a real JTAG boot, make sure that:

- A Linux or Windows Station binary is installed or extracted.
- XSDB is installed on the Station computer as part of Vivado, Vitis, or the
  standalone Hardware Server package.
- A local or remote Xilinx `hw_server` is running and reachable, normally on
  TCP port 3121.
- The JTAG adapter is visible to `hw_server`.
- For automatic UART capture, the FT2232H channel B tty/COM port is visible on
  the Station computer and its EEPROM serial is unique. UART is optional for
  JTAG boot, and any enumerated serial port can be selected manually.
- The target board can reach the Station computer over the provisioning
  network.
- UDP port 69 is available for the Station's TFTP listener.
- An `mnc-station-artifact` v2 `.tar.gz` file is available. For demo board this is
  normally `demo-board-jtag-image.tar.gz` from the Yocto
  `build/export/provision-image/` directory.

Only one provisioning job runs at a time. Selecting multiple JTAG devices
creates one job per device; the jobs remain queued and boot sequentially so
they do not compete for the Station's TFTP listener.

## Validate an artifact first

Artifact inspection validates the archive, manifest, paths, file types, sizes,
modes, and SHA-256 values without saving the artifact:

```bash
mpsd inspect demo-board-jtag-image.tar.gz
```

Use JSON output for scripts:

```bash
mpsd inspect --json demo-board-jtag-image.tar.gz
```

A valid artifact is not automatically trusted or authorized for production.
The optional `manifest.sig` is retained, but signature verification belongs to
the future protected release pipeline.

## Start the Station on Linux

The Station searches `PATH` first, followed by Xilinx environment variables
and versioned Vivado, Vitis, and standalone Hardware Server (`HWSRVR`)
installations below `/opt/Xilinx`. Set an explicit path only for a nonstandard
installation or to select a specific XSDB version:

```bash
export MPS_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb
mpsd serve --data-dir="$PWD/.station-dev" --open-browser
```

TFTP normally uses UDP port 69. A portable binary needs the narrow low-port
capability when it is run as a regular user:

```bash
sudo setcap cap_net_bind_service=+ep /path/to/mpsd
/path/to/mpsd serve --data-dir="$PWD/.station-dev" --open-browser
```

Rebuilding or replacing the binary removes that file capability, so apply it
again after an upgrade. For UI-only testing, use an unprivileged port:

```bash
mpsd serve --tftp-listen=127.0.0.1:6969 --open-browser
```

A real board still sends its initial TFTP request to port 69, so port 6969 is
only useful for development and automated tests.

### Linux system services

The Debian package installs and enables `mps.service`. It pulls in the
companion `mps-hw-server.service`, which starts a local Xilinx
`hw_server` automatically when one is available. Both services run as the
dedicated `mps` account. Its home and writable working directory are
`/var/lib/mps`, so neither service depends on a developer's user name
or home directory.

The services have a smaller `PATH` than an interactive shell, but they
automatically search versioned Vivado, Vitis, and standalone Hardware Server
(`HWSRVR`) installations below `/opt/Xilinx`. Override either detected
executable in `/etc/default/mps` only when necessary:

```text
MPS_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb
MPS_HW_SERVER=/opt/Xilinx/2025.2/HWSRVR/bin/hw_server
```

The managed hardware server listens on `tcp:127.0.0.1:3121` by default. This
keeps the unauthenticated JTAG control port off the LAN while allowing the
local Station to use it. Set `MPS_HW_SERVER_LISTEN` only when another trusted
computer must connect directly:

```text
MPS_HW_SERVER_LISTEN=tcp:192.0.2.40:3121
```

Then restart and inspect the service:

```bash
sudo systemctl restart mps
systemctl status mps
systemctl status mps-hw-server
journalctl -u mps -u mps-hw-server -f
```

Do not run a second local `hw_server` on port 3121. Disable any hand-written
unit before starting the packaged companion service. XSDB is not a daemon;
the Station starts it on demand for target discovery and provisioning jobs.

The package adds the `mps` service account to `dialout` and `plugdev`
when those groups exist, allowing it to open serial tty and group-accessible
JTAG devices after installation. Xilinx's cable-driver udev rules must still
be installed. For a portable launch, add your own user to the distribution's
device groups and begin a new login session. Keep normal device permissions;
do not make `/dev/ttyUSB*` world-writable.

The service listens on every IPv4 interface. A direct local connection at
`http://127.0.0.1:8042/` skips token verification. From another computer, open:

```text
http://<station-ip-or-hostname>:8042/
```

Use `http://`, not `https://`, unless you configured a TLS reverse proxy. The
LAN connections ask for the automatically generated API token. Any `172.x`,
`192.168.x`, or other non-loopback address requires authentication even when
it belongs to the Station computer. Read the token with:

```bash
sudo mpsd token --service
```

The command prints only the token, making it convenient to paste into the
dashboard or capture in a script. Treat its output as a password. The backing
file remains `/var/lib/mps/api-token`. If the service uses a custom
token file, pass its path with `--api-token-file`.

If a managed service token is exposed, rotate it and restart the service:

```bash
sudo mpsd token --rotate --service
sudo systemctl restart mps
```

The rotation command prints the replacement token. Update the dashboard,
`MncBuildPreset.yaml`, and other clients before restarting. The running agent
continues using the previous in-memory token until it is restarted. For a
portable Linux or Windows installation, use `mpsd token --rotate` (or
`mpsd.exe token --rotate`) and restart the foreground agent.

Persistent artifacts, jobs, and the managed token are stored under
`/var/lib/mps`. If the page is still unreachable, allow TCP port 8042
through the Station firewall for the trusted provisioning network.

The Debian package installs Bash completion for commands and options. Start a
new Bash shell after installation, or load it immediately with:

```bash
source /usr/share/bash-completion/completions/mps
source /usr/share/bash-completion/completions/mpsd
```

You can then use completion after `mps` and the daemon administration commands `mpsd serve`,
`mpsd inspect`, and `mpsd token`. Linux portable archives
provide the same script at `completion/mps.bash` inside the extracted
directory.

## Start the Station on Windows

The MSI installs `mps.exe` and `mpsd.exe` and starts the native SCM service
`mps` as LocalService. State and its token live under
`%ProgramData%\Monutchee\MPS`, protected for LocalService, SYSTEM, and
Administrators. The Start menu launches `mps`, the terminal UI. An ordinary
client can query service status; startup requires the Windows account's
existing SCM authorization. If denied, use an administrator terminal:

```powershell
sc.exe start mps
mps
```

The daemon searches `PATH`, Xilinx environment variables, and `C:\Xilinx` for
vendor tools. Set `MPS_XSDB`/`MPS_HW_SERVER` as machine environment settings
for nonstandard installs, then restart the service. The service owns an
optional hw_server child in a kill-on-close Job Object; absent vendor tools
leave the dashboard/API available. Allow TCP 8042 and the required TFTP traffic
on the trusted provisioning network.

For foreground debugging, use an explicit writable directory:

```powershell
.\mpsd.exe serve --data-dir="$env:TEMP\mps-dev" --open-browser
```

Stop foreground mode with Ctrl+C. Install a suitable FTDI virtual COM driver
and close other applications owning a console port. Migrate previous per-user
state explicitly; see [migration](MIGRATION.md).

## Boot from the browser dashboard

1. Open `http://127.0.0.1:8042/` locally, or
   `http://<station-ip-or-hostname>:8042/` remotely.
2. Enter the Station API token when prompted. Direct `127.0.0.1` and `::1`
   connections do not prompt for one.
3. Confirm that the agent reports **XSDB available**.
4. Drop `demo-board-jtag-image.tar.gz` onto the artifact area, or select it using
   the file picker.
5. Select the imported artifact.
6. Enter the hardware-server URL. Use `tcp:127.0.0.1:3121` for a local
   `hw_server`, or for example `tcp:192.0.2.40:3121` for a remote server.
7. Scan the hardware server and select one or more JTAG devices. Every detected
   JTAG target remains selectable. For each target, keep its automatic FTDI
   match, choose a specific tty/COM port, or select **No serial capture (JTAG
   only)**. Multiple devices create sequential jobs using the same artifact;
   choose a different serial port for each device.
8. Verify the prefilled Station TFTP IPv4 address. The dashboard selects the
   first usable interface reported by the operating system and lists every
   detected interface/IP. On a multi-NIC Station, select an entry or manually
   type the address the target board can route to; never use `127.0.0.1` for a
   physical board.
9. Optionally enter the board IPv4 address. When supplied, the TFTP server
   rejects requests from other client addresses. Leave it empty for a
   multi-device boot so DHCP can assign each board a unique address.
10. When a serial port is selected, confirm the baud rate. Start the jobs and
    follow their ordered event logs. Requested UART capture opens before XSDB
    and is retained with the job; JTAG-only jobs have no UART dependency.
11. Use the Serial console panel to connect interactively. One browser or API
    client can type at a time; additional clients attach read-only. Select a
    completed job and load its retained transcript from the same panel.

A job succeeds only when XSDB exits successfully and every file below the
artifact's `tftpRoot` has been requested and transferred. Canceling a queued
or running job is supported from the dashboard.

Starting a JTAG job can reset or reconfigure attached hardware. Confirm the
selected cable and board before starting it.

The artifact card displays the retained archive's full SHA-256 digest and the
manifest's localized build date/time. The digest is also the artifact ID used
by the API and content-addressed store.

Targeted jobs require a Station artifact built with the multi-device loader.
If an older imported artifact is selected, rebuild `demo-board-jtag-image` in Yocto
and import the newly generated archive; the Station rejects the old loader
before touching hardware.

## Boot with `mnc deploy`

The demo board workspace command uses the same HTTP API as the browser. Start the
Station first, then configure `MncBuildPreset.yaml` in the product workspace:

```yaml
version: 1
stages:
  deploy:
    type: jtag
    station_url: http://127.0.0.1:8042
    xilinx_hw_server_url: tcp:192.0.2.20:3121
    tftp_server_ip: 192.0.2.19
    board_ip: null
```

Run:

```bash
./mnc deploy
```

For demo board, `mnc` reads the stable artifact from:

```text
yocto-build/build/export/provision-image/demo-board-jtag-image.tar.gz
```

It validates Station availability, uploads the artifact, queues the job, and
prints job events until completion. Command-line settings override the preset:

```bash
./mnc deploy jtag \
  --station-url http://127.0.0.1:8042 \
  --xilinx-hw-server-url tcp:192.0.2.20:3121 \
  --tftp-server-ip 192.0.2.19 \
  --board-ip 192.0.2.10
```

For the external `mnc` client, set its own `MNC_STATION_TOKEN` before running `mnc deploy` when the Station API uses
bearer authentication.

## Runtime configuration

The commonly used settings are:

| Command option | Environment variable | Default |
| --- | --- | --- |
| `--http-listen` | `MPS_HTTP_LISTEN` | `0.0.0.0:8042` |
| `--tftp-listen` | `MPS_TFTP_LISTEN` | `:69` |
| `--data-dir` | `MPS_DATA_DIR` | Machine-wide state directory |
| `--xsdb-path` | `MPS_XSDB` | Search `PATH`, Xilinx environment, then default install root |
| `--api-token-file` | `MPS_TOKEN_FILE` | Not set |
| — | `MPS_TOKEN` | Not set |
| `--job-timeout` | — | `10m` |
| `--max-artifact-bytes` | — | 2 GiB |
| `--max-unpacked-bytes` | — | 8 GiB |
| `--serial-baud` | `MPS_SERIAL_BAUD` | `115200` |
| `--max-console-log-bytes` | `MPS_MAX_CONSOLE_LOG_BYTES` | 16 MiB |

Run `mpsd serve -help` for the complete option list.

The Linux companion service runs `mpsd hw-server`. That command accepts
`--hw-server-path` (`MPS_HW_SERVER`) and `--listen`
(`MPS_HW_SERVER_LISTEN`). The defaults are automatic discovery and
`tcp:127.0.0.1:3121`; `--check` reports whether an executable can be resolved
without starting it.

The machine-wide state defaults are `/var/lib/mps` on Linux and
`%ProgramData%\Monutchee\MPS` on Windows. Foreground development can select
an explicit writable `--data-dir`. A process-wide OS lock prevents two daemons
from recovering or modifying the same state concurrently.

The data directory contains retained artifacts and persistent job/event
records. Do not edit it while the Station is running.

## API authentication and remote access

The default listener accepts IPv4 LAN connections. To keep remote provisioning
control authenticated, the Station creates a random 48-character token at
`<data-dir>/api-token` when no token was explicitly configured. A loopback-only
listener can run without authentication:

```bash
mpsd serve --http-listen=127.0.0.1:8042
```

To provide your own token on Linux, create a file readable only by the service
account and set `MPS_TOKEN_FILE` in `/etc/default/mps`. For a
foreground development session, an environment variable can be used:

```bash
export MPS_TOKEN='replace-with-a-long-random-token'
mpsd serve --http-listen=192.0.2.30:8042
```

The browser asks for the token, and command-line clients read
`MPS_TOKEN`. A bearer token authenticates requests but does not encrypt
HTTP traffic. Put a trusted TLS or mutually authenticated reverse proxy in
front of the Station before allowing control from outside a trusted station
network. The health endpoint remains public.

The full automation contract is documented in
[`api/openapi.yaml`](../api/openapi.yaml). Serial identity, WebSocket framing,
leases, and capture behavior are explained in
[`SERIAL_CONSOLE.md`](SERIAL_CONSOLE.md).

## Troubleshooting

### The dashboard cannot be reached remotely

- Include the port and use `http://<station-ip-or-hostname>:8042/`. The agent
  does not provide HTTPS directly.
- Run `sudo ss -ltnp | grep ':8042'` and confirm the listener is
  `0.0.0.0:8042`.
- Check `systemctl status mps` and
  `journalctl -u mps --no-pager -n 100`.
- Allow TCP port 8042 through the firewall only on the trusted provisioning
  network.
- Do not launch a second `mps` process while the packaged systemd
  service owns the port.

### XSDB is unavailable

- Check the dashboard's XSDB status tooltip or service log for the searched
  locations.
- Run `xsdb -eval "puts ok"` from the same shell or service account. Remember
  that the systemd service does not inherit your interactive shell's `PATH`.
- The automatic search checks `PATH`, `XILINX_VITIS`, `XILINX_VIVADO`, and
  `/opt/Xilinx` on Linux or `C:\Xilinx` on Windows.
- Set `MPS_XSDB` or pass `--xsdb-path` with the full `xsdb`/`xsdb.bat` path to
  override automatic discovery.
- When using the Linux service, update `/etc/default/mps` and restart
  the service.
- XSDB must be installed on the Station computer even when `hw_server` is
  remote.

### The managed hardware server is unavailable

- Run `sudo -u mps mpsd hw-server --check` to verify discovery
  using the service identity.
- Inspect `systemctl status mps-hw-server` and
  `journalctl -u mps-hw-server --no-pager -n 100`.
- Set `MPS_HW_SERVER` in `/etc/default/mps` when the executable uses a
  nonstandard path, then restart `mps`.
- Install the Xilinx cable-driver udev rules so the unprivileged service
  account can access the JTAG adapter. Do not run `hw_server` as root to work
  around missing device rules.
- Stop any other `hw_server` instance already listening on TCP port 3121.

### The TFTP listener cannot start

- Check whether another TFTP service already owns UDP port 69.
- On Linux, use the Debian service or apply `cap_net_bind_service` to the
  portable binary.
- On Windows, allow the Station executable through the firewall on the trusted
  network profile.

### XSDB connects, but the board does not fetch files

- Verify that `tftp_server_ip` is the Station interface reachable from the
  board, not the `hw_server` address unless they are the same machine.
- Check the provisioning VLAN, routing, host firewall, and board netmask.
- If `board_ip` is configured, make sure it matches the source address used by
  the board's TFTP client.
- Inspect the job event log to see which expected files were requested.

### The remote hardware server cannot be reached

- Confirm that `hw_server` is running and listening on the configured port.
- Use the exact `tcp:<host>:<port>` form.
- Test TCP connectivity from the Station computer and check firewalls between
  the two hosts.

### A JTAG device has no paired serial console

- Confirm the adapter is an FT2232H with VID:PID `0403:6010` and that channel B
  appears as `/dev/ttyUSB*` or a Windows COM port on the Station computer.
- A remote `hw_server` does not make its host's COM port available remotely;
  the matching UART must be attached to the Station host.
- Program a unique, non-empty FTDI EEPROM serial. Duplicate serials are
  excluded from automatic association because they cannot be paired safely.
- If the correct port is listed (for example `COM3`) but cannot be matched,
  select it explicitly for that JTAG target. This is an operator override;
  verify the physical cable-to-port relationship first. Select **No serial
  capture (JTAG only)** when UART is not needed.
- On Linux, check the Station user's serial-port group membership and tty
  permissions. On Windows, close other terminal applications that may own the
  COM port.
- A baud conflict means another Station attachment already has the port open
  at a different rate. Disconnect it or select the active rate.

### A job remains queued

The Station intentionally serializes hardware access. Wait for the active job
to finish or cancel it. If the agent was restarted during a job, that
interrupted job is marked failed and the next queued job can run.

## Current scope

The Station currently executes Xilinx `jtag-boot` artifacts with the
`xilinx-xsdb` executor. Cloud enrollment, manufacturing records, secure-boot
signing, Azure IoT key issuance, NXP UUU, and WIC media flashing are future
components and are not implemented here.
