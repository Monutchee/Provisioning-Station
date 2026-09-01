# Serial console API

Provisioning Station exposes locally enumerated Linux tty and Windows COM
ports through HTTP API v1. The browser uses this API; future cloud software can
use the same boundary without accessing a host serial device directly.

## Supported hardware and identity

The native discovery backend lists every serial port reported by the operating
system. It automatically associates JTAG and UART only for FT2232H devices with
USB VID:PID `0403:6010`, a channel B interface, and a non-empty, unique EEPROM
serial number. Channel A remains owned by JTAG.

Linux reports serial ports as devices such as `/dev/ttyUSB1`. Windows reports
them as COM ports such as `COM3` or `COM7`. Automatically matchable FTDI ports
receive an ID derived from VID, PID, shared EEPROM serial, and channel. Other
ports receive an opaque ID derived from their current operating-system name;
clients must refresh discovery after re-enumeration rather than persist a
manual association indefinitely.

`GET /api/v1/xilinx/targets` compares each XSDB cable serial with the local
FTDI EEPROM serial and returns:

- `serialAssociation: matched` plus `serialPort` when there is one safe match;
- `not_found` when the UART is not visible on the Station host; or
- `ambiguous` when the EEPROM serial is duplicated.

A remote `hw_server` target is associated automatically only when its matching
UART is attached locally to the Station. JTAG discovery and boot do not depend
on UART availability. The browser lets an operator keep serial capture off,
use the automatic match, or explicitly choose a different tty/COM port for
each target.

## Discover and attach

List local serial ports:

```http
GET /api/v1/serial/ports
Authorization: Bearer <station-token>
```

Reserve a live attachment configured as 8 data bits, no parity, and one stop
bit:

```http
POST /api/v1/serial/sessions
Content-Type: application/json
Authorization: Bearer <station-token>

{
  "portId": "<stable-port-id>",
  "baudRate": 115200,
  "access": "controller"
}
```

`baudRate` may be 300 through 4000000 and uses the Station default when
omitted. Multiple `observer` attachments can share one port. Exactly one
`controller` can write. All attachments sharing a physical port must use the
same baud rate until the last attachment disconnects. A busy port reports that
rate as `activeBaudRate`, allowing another client to join as an observer.

The response contains a random session ID, a one-time attach token, and a
same-origin WebSocket path. It has `Cache-Control: no-store`. Open that path
within 30 seconds, then send this as the first WebSocket text message within
five seconds:

```json
{"type":"attach","token":"<one-time-attach-token>"}
```

The server replies with one text status message:

```json
{"type":"ready","portId":"<stable-port-id>","access":"controller","baudRate":115200}
```

After `ready`, server-to-client binary frames contain unmodified UART RX bytes.
A controller sends UART TX bytes in binary frames; an observer sends no data.
Client messages are limited to 64 KiB. The attach token is intentionally sent
in the first frame rather than a URL so it does not enter proxy access logs or
browser history. WebSocket upgrades also enforce the browser same-origin
policy.

The backend keeps the most recent 256 KiB by default and replays it to a new
live attachment. Slow consumers are disconnected instead of blocking the
physical serial reader or other clients.

## Capture with a provisioning job

Add `serialConsole` to `POST /api/v1/jobs`:

```json
{
  "artifactId": "<artifact-sha256>",
  "hwServerUrl": "tcp:127.0.0.1:3121",
  "tftpServerIp": "192.0.2.10",
  "targetId": "3",
  "targetCableSerial": "BOARD-A",
  "targetDeviceIndex": "0",
  "serialConsole": {
    "portId": "<stable-port-id>",
    "baudRate": 115200
  }
}
```

The default `selection` is `matched`, so the Xilinx runner verifies that
`portId` belongs to `targetCableSerial`. To record an explicit operator
override, send a currently enumerated port with `selection: "manual"`:

```json
{
  "serialConsole": {
    "portId": "<manually-selected-port-id>",
    "baudRate": 115200,
    "selection": "manual"
  }
}
```

Manual selection confirms only that the port is visible on this Station; it
does not claim that the tty/COM port belongs to the selected JTAG cable, and a
warning is recorded in the job event log. The backend opens RX-only capture
before XSDB is allowed to run. If the requested serial port cannot be
discovered, configured, or opened, the job fails without touching the target.
Omit `serialConsole` to run JTAG without any UART dependency.

The persisted job record includes `serialCapture` metadata. Download retained
raw bytes with:

```http
GET /api/v1/jobs/{jobId}/serial-transcript
```

Only received bytes are recorded; operator input is not added to the job
transcript. The default per-job limit is 16 MiB. On reaching it, capture is
marked `truncated`, retention stops, and live streaming continues. Change the
limit with `--max-console-log-bytes` or
`MNC_STATION_MAX_CONSOLE_LOG_BYTES`.

## Host access

On Linux, the Station process needs read/write access to the selected tty. The
Debian package adds its service account to `dialout` when that group exists.
For a portable launch, add the invoking user to the distribution's serial-port
group, then start a new login session. Do not make the tty world-writable.

On Windows, install the device's virtual COM port driver (the FTDI driver for
FT2232H adapters) if it does not appear in Device Manager. Only one
operating-system process can normally open a COM port, so close external
terminal programs before connecting through the Station.

The complete schemas and error responses are in
[`../api/openapi.yaml`](../api/openapi.yaml).
