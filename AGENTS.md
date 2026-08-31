# Provisioning Station guidance

## Scope and boundaries

- This repository owns the open-source, local-first Provisioning Station
  agent, its embedded browser UI, and local hardware executors.
- Keep cloud enrollment, production databases, key generation, release
  signing services, and factory orchestration out of this repository.
- The public HTTP API is the automation boundary for the browser UI, `mnc`,
  and future cloud software. Do not make the UI call hardware tools directly.
- Artifact parsing is a security boundary. Reject links, devices, duplicate or
  unsafe paths, unrecorded payloads, checksum mismatches, and resource-limit
  violations before an executor sees an artifact.
- Vendor-neutral artifact and job behavior belongs in generic packages.
  Xilinx XSDB policy belongs in the Xilinx runner; future NXP/UUU support must
  use another runner rather than adding vendor branches to the HTTP layer.

## Local development

- Run `go test ./...`, `go vet ./...`, and both Linux and Windows builds before
  handing off a change that affects runtime behavior.
- Keep the production binary dependency-light and embed the browser assets so
  installation never requires Node.js or a separate web server.
- The default HTTP listener accepts IPv4 LAN connections. A non-loopback
  listener must always require an explicit or automatically managed API token.
- Never log API tokens, signing material, or future device credentials.
- Physical JTAG tests reset connected hardware. Resolve the selected target
  and obtain explicit user authorization before running one.
