# Provisioning Station user guide

This guide explains how to run the Monutchee Provisioning Station, import a
Station artifact, and boot an MSAP1 board through Xilinx JTAG. For compiler and
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
   | XSDB             | read-only TFTP       | FTDI channel B UART
   v                  v                      v
Xilinx hw_server ── JTAG ──> target board <──┘
```

XSDB runs on the Station computer. The Xilinx `hw_server` can run on that same
computer or on another reachable computer. During boot, the target board must
be able to reach the Station computer's TFTP IPv4 address.

## Prerequisites

Before starting a real JTAG boot, make sure that:

- A Linux or Windows Station binary is installed or extracted.
- XSDB is installed on the Station computer as part of Vivado or Vitis.
- A local or remote Xilinx `hw_server` is running and reachable, normally on
  TCP port 3121.
- The JTAG adapter is visible to `hw_server`.
- The FT2232H channel B tty/COM port is visible on the Station computer and
  its EEPROM serial is unique. Channel A remains the JTAG interface.
- The target board can reach the Station computer over the provisioning
  network.
- UDP port 69 is available for the Station's TFTP listener.
- An `mnc-station-artifact` v2 `.tar.gz` file is available. For MSAP1 this is
  normally `msap1-jtag-image.tar.gz` from the Yocto
  `build/export/provision-image/` directory.

Only one provisioning job runs at a time. Selecting multiple JTAG devices
creates one job per device; the jobs remain queued and boot sequentially so
they do not compete for the Station's TFTP listener.

## Validate an artifact first

Artifact inspection validates the archive, manifest, paths, file types, sizes,
modes, and SHA-256 values without saving the artifact:

```bash
mnc-station inspect msap1-jtag-image.tar.gz
```

Use JSON output for scripts:

```bash
mnc-station inspect --json msap1-jtag-image.tar.gz
```

A valid artifact is not automatically trusted or authorized for production.
The optional `manifest.sig` is retained, but signature verification belongs to
the future protected release pipeline.

## Start the Station on Linux

The Station searches `PATH` first, followed by Xilinx environment variables
and versioned installations below `/opt/Xilinx`. Set an explicit path only for
a nonstandard installation or to select a specific XSDB version:

```bash
export MNC_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb
mnc-station serve --open-browser
```

TFTP normally uses UDP port 69. A portable binary needs the narrow low-port
capability when it is run as a regular user:

```bash
sudo setcap cap_net_bind_service=+ep /path/to/mnc-station
/path/to/mnc-station serve --open-browser
```

Rebuilding or replacing the binary removes that file capability, so apply it
again after an upgrade. For UI-only testing, use an unprivileged port:

```bash
mnc-station serve --tftp-listen=127.0.0.1:6969 --open-browser
```

A real board still sends its initial TFTP request to port 69, so port 6969 is
only useful for development and automated tests.

### Linux system service

The Debian package installs and enables `mnc-station.service`. The service has
a smaller `PATH` than an interactive shell, but it automatically searches
versioned Vivado and Vitis installations below `/opt/Xilinx`. To override the
detected executable, configure XSDB in `/etc/default/mnc-station`:

```text
MNC_XSDB=/opt/Xilinx/Vitis/2025.2/bin/xsdb
```

Then restart and inspect the service:

```bash
sudo systemctl restart mnc-station
systemctl status mnc-station
journalctl -u mnc-station -f
```

The package adds the `mnc-station` service account to the `dialout` group when
that group exists, allowing it to open FTDI tty devices after installation.
For a portable launch, add your own user to the distribution's serial-port
group and begin a new login session. Keep normal device permissions; do not
make `/dev/ttyUSB*` world-writable.

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
sudo mnc-station token --service
```

The command prints only the token, making it convenient to paste into the
dashboard or capture in a script. Treat its output as a password. The backing
file remains `/var/lib/mnc-station/api-token`. If the service uses a custom
token file, pass its path with `--api-token-file`.

If a managed service token is exposed, rotate it and restart the service:

```bash
sudo mnc-station token --rotate --service
sudo systemctl restart mnc-station
```

The rotation command prints the replacement token. Update the dashboard,
`MncBuildPreset.yaml`, and other clients before restarting. The running agent
continues using the previous in-memory token until it is restarted. For a
portable Linux or Windows installation, use `mnc-station token --rotate` (or
`mnc-station.exe token --rotate`) and restart the foreground agent.

Persistent artifacts, jobs, and the managed token are stored under
`/var/lib/mnc-station`. If the page is still unreachable, allow TCP port 8042
through the Station firewall for the trusted provisioning network.

The Debian package installs Bash completion for commands and options. Start a
new Bash shell after installation, or load it immediately with:

```bash
source /usr/share/bash-completion/completions/mnc-station
```

You can then use completion after `mnc-station`, `mnc-station serve`,
`mnc-station inspect`, and `mnc-station token`. Linux portable archives
provide the same script at `completion/mnc-station.bash` inside the extracted
directory.

## Start the Station on Windows

Open PowerShell or a Xilinx command prompt. The Station searches `PATH`, Xilinx
environment variables, and versioned Vivado/Vitis installations below
`C:\Xilinx`. Provide the path to `xsdb.bat` only when automatic discovery does
not select the desired installation:

```powershell
$env:MNC_XSDB = "C:\Xilinx\Vitis\2025.2\bin\xsdb.bat"
.\mnc-station.exe serve --open-browser
```

The browser opens `http://127.0.0.1:8042/` without requiring a token, while
remote clients can use the Windows computer's hostname or IP address with port
8042 and must authenticate. Show the generated token with
`mnc-station.exe token`; its backing file is
`%AppData%\Monutchee\Provisioning-Station\api-token`. Allow the Station
executable to receive TCP port 8042 and TFTP traffic through Windows Defender
Firewall on the trusted provisioning network. If the MSI Start menu shortcut
is used, configure `MNC_XSDB` as a persistent user or system environment
variable, or make XSDB available on `PATH`.

The FTDI virtual COM port driver must be installed and the channel B COM port
must not already be open in another terminal program. Station discovers the
COM number dynamically and uses the adapter's EEPROM serial for stable
pairing.

Stop a foreground Station with `Ctrl+C` on either platform.

## Boot from the browser dashboard

1. Open `http://127.0.0.1:8042/` locally, or
   `http://<station-ip-or-hostname>:8042/` remotely.
2. Enter the Station API token when prompted. Direct `127.0.0.1` and `::1`
   connections do not prompt for one.
3. Confirm that the agent reports **XSDB available**.
4. Drop `msap1-jtag-image.tar.gz` onto the artifact area, or select it using
   the file picker.
5. Select the imported artifact.
6. Enter the hardware-server URL. Use `tcp:127.0.0.1:3121` for a local
   `hw_server`, or for example `tcp:192.0.2.40:3121` for a remote server.
7. Scan the hardware server and select one or more JTAG devices. Cable serial,
   device name/index, XSDB target ID, and its paired local UART identify each
   entry. A target without one safe FTDI channel B match is disabled. Multiple
   devices create sequential jobs using the same artifact.
8. Verify the prefilled Station TFTP IPv4 address. The dashboard selects the
   first usable interface reported by the operating system and lists every
   detected interface/IP. On a multi-NIC Station, select an entry or manually
   type the address the target board can route to; never use `127.0.0.1` for a
   physical board.
9. Optionally enter the board IPv4 address. When supplied, the TFTP server
   rejects requests from other client addresses. Leave it empty for a
   multi-device boot so DHCP can assign each board a unique address.
10. Confirm the serial baud rate, then start the jobs and follow their ordered
    event logs. UART capture opens before XSDB and is retained with the job.
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
If an older imported artifact is selected, rebuild `msap1-jtag-image` in Yocto
and import the newly generated archive; the Station rejects the old loader
before touching hardware.

## Boot with `mnc deploy`

The MSAP1 workspace command uses the same HTTP API as the browser. Start the
Station first, then configure `MncBuildPreset.yaml` in the product workspace:

```yaml
version: 1
stages:
  deploy:
    type: jtag
    station_url: http://127.0.0.1:8042
    xilinx_hw_server_url: tcp:172.30.19.20:3121
    tftp_server_ip: 172.30.19.19
    board_ip: null
```

Run:

```bash
./mnc deploy
```

For MSAP1, `mnc` reads the stable artifact from:

```text
yocto-build/build/export/provision-image/msap1-jtag-image.tar.gz
```

It validates Station availability, uploads the artifact, queues the job, and
prints job events until completion. Command-line settings override the preset:

```bash
./mnc deploy jtag \
  --station-url http://127.0.0.1:8042 \
  --xilinx-hw-server-url tcp:172.30.19.20:3121 \
  --tftp-server-ip 172.30.19.19 \
  --board-ip 172.30.19.10
```

Set `MNC_STATION_TOKEN` before running `mnc deploy` when the Station API uses
bearer authentication.

## Runtime configuration

The commonly used settings are:

| Command option | Environment variable | Default |
| --- | --- | --- |
| `--http-listen` | `MNC_STATION_HTTP_LISTEN` | `0.0.0.0:8042` |
| `--tftp-listen` | `MNC_STATION_TFTP_LISTEN` | `:69` |
| `--data-dir` | `MNC_STATION_DATA_DIR` | Platform user configuration directory |
| `--xsdb-path` | `MNC_XSDB` | Search `PATH`, Xilinx environment, then default install root |
| `--api-token-file` | `MNC_STATION_TOKEN_FILE` | Not set |
| — | `MNC_STATION_TOKEN` | Not set |
| `--job-timeout` | — | `10m` |
| `--max-artifact-bytes` | — | 2 GiB |
| `--max-unpacked-bytes` | — | 8 GiB |
| `--serial-baud` | `MNC_STATION_SERIAL_BAUD` | `115200` |
| `--max-console-log-bytes` | `MNC_STATION_MAX_CONSOLE_LOG_BYTES` | 16 MiB |

Run `mnc-station serve -help` for the complete option list.

On a normal user launch, data is stored below the operating system's user
configuration directory. Typical locations are:

- Linux: `${XDG_CONFIG_HOME:-$HOME/.config}/Monutchee/Provisioning-Station`
- Windows: `%AppData%\Monutchee\Provisioning-Station`
- Debian service: `/var/lib/mnc-station`

The data directory contains retained artifacts and persistent job/event
records. Do not edit it while the Station is running.

## API authentication and remote access

The default listener accepts IPv4 LAN connections. To keep remote provisioning
control authenticated, the Station creates a random 48-character token at
`<data-dir>/api-token` when no token was explicitly configured. A loopback-only
listener can run without authentication:

```bash
mnc-station serve --http-listen=127.0.0.1:8042
```

To provide your own token on Linux, create a file readable only by the service
account and set `MNC_STATION_TOKEN_FILE` in `/etc/default/mnc-station`. For a
foreground development session, an environment variable can be used:

```bash
export MNC_STATION_TOKEN='replace-with-a-long-random-token'
mnc-station serve --http-listen=192.0.2.30:8042
```

The browser asks for the token, and command-line clients read
`MNC_STATION_TOKEN`. A bearer token authenticates requests but does not encrypt
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
- Check `systemctl status mnc-station` and
  `journalctl -u mnc-station --no-pager -n 100`.
- Allow TCP port 8042 through the firewall only on the trusted provisioning
  network.
- Do not launch a second `mnc-station` process while the packaged systemd
  service owns the port.

### XSDB is unavailable

- Check the dashboard's XSDB status tooltip or service log for the searched
  locations.
- Run `xsdb -eval "puts ok"` from the same shell or service account. Remember
  that the systemd service does not inherit your interactive shell's `PATH`.
- The automatic search checks `PATH`, `XILINX_VITIS`, `XILINX_VIVADO`, and
  `/opt/Xilinx` on Linux or `C:\Xilinx` on Windows.
- Set `MNC_XSDB` or pass `--xsdb-path` with the full `xsdb`/`xsdb.bat` path to
  override automatic discovery.
- When using the Linux service, update `/etc/default/mnc-station` and restart
  the service.
- XSDB must be installed on the Station computer even when `hw_server` is
  remote.

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
  rejected because they cannot be associated safely.
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
