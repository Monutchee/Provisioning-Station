# Building Provisioning Station

This guide covers developer builds, tests, cross-compilation, and installable
packages for Linux and Windows. For operating instructions, see
[README.md](README.md).

## Build design

Provisioning Station is a Go application with an embedded HTML/CSS/JavaScript
dashboard. It has no third-party Go modules and does not require Node.js. The
same source builds one self-contained executable for Linux or Windows.

Vivado, Vitis, XSDB, and `hw_server` are runtime dependencies for Xilinx JTAG
jobs; they are not needed to compile or unit-test the program.

## Source and tool requirements

Required on every development platform:

- Go 1.27 or newer.
- Git when version metadata or release packaging is required.

Additional Linux release tools:

- GNU Make for the convenience targets.
- Bash, `tar`, and `zip` for release archives.
- `dpkg-deb` and `adduser` metadata support for Debian packages.
- `libcap` tools to grant a portable binary access to UDP port 69.

Additional Windows MSI tools:

- PowerShell.
- A .NET SDK.
- WiX Toolset CLI 6.0.0.

Clone the repository and enter it:

```bash
git clone https://github.com/Monutchee/Provisioning-Station.git
cd Provisioning-Station
```

Confirm the compiler version:

```bash
go version
```

## Build and test on Linux

Run the full normal validation:

```bash
make check
make test-race
```

`make check` verifies formatting, runs `go vet`, and runs all tests. The race
target additionally checks concurrent job, API, and TFTP behavior.

Build the host executable:

```bash
make build
./dist/mnc-station version
```

The output is `dist/mnc-station`. The default build reports version `dev`.
Supply release-shaped metadata when needed:

```bash
make build \
  VERSION=0.1.0 \
  COMMIT="$(git rev-parse --short=12 HEAD)" \
  BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

The equivalent direct Go command is:

```bash
mkdir -p dist
CGO_ENABLED=0 go build -trimpath -o dist/mnc-station ./cmd/mnc-station
```

`CGO_ENABLED=0` is used by release builds so the resulting executable has no C
runtime dependency.

### Linux cross-build matrix

Build Linux amd64, Linux arm64, and Windows amd64 from a Linux host:

```bash
make cross
```

Outputs:

```text
dist/mnc-station-linux-amd64
dist/mnc-station-linux-arm64
dist/mnc-station-windows-amd64.exe
```

A successful cross-build proves that the Windows source compiles. Native
Windows CI should still run the tests because command-wrapper and socket
behavior are operating-system specific.

## Build and test on Windows

Run these commands in PowerShell from the repository root. GNU Make is not
required for a native Windows build.

```powershell
go test ./...
go vet ./...

$Unformatted = gofmt -l .
if ($Unformatted) {
    $Unformatted
    throw "Go source is not formatted"
}
```

Build the executable:

```powershell
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -o dist\mnc-station.exe .\cmd\mnc-station
.\dist\mnc-station.exe version
```

Build with version metadata:

```powershell
$Version = "0.1.0"
$Commit = (git rev-parse --short=12 HEAD).Trim()
$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$Ldflags = "-s -w -X main.version=$Version -X main.commit=$Commit -X main.buildDate=$BuildDate"

go build -trimpath -ldflags $Ldflags `
    -o dist\mnc-station.exe .\cmd\mnc-station
```

The test suite uses unprivileged loopback ports and does not connect to a real
board. A physical JTAG smoke test is separate and must only be run when the
selected hardware may safely be reset.

### Cross-compile Linux from Windows

PowerShell can also produce a Linux executable:

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -trimpath -o dist\mnc-station-linux-amd64 .\cmd\mnc-station

Remove-Item Env:GOOS
Remove-Item Env:GOARCH
```

The Linux binary cannot be executed on Windows; test it on Linux or in CI.

## Build release archives and Debian packages

Run the release builder on Linux:

```bash
./packaging/build-release.sh 0.1.0 release
```

It builds:

```text
release/mnc-station_0.1.0_linux_amd64.tar.gz
release/mnc-station_0.1.0_linux_arm64.tar.gz
release/mnc-station_0.1.0_amd64.deb
release/mnc-station_0.1.0_arm64.deb
release/mnc-station_0.1.0_windows_amd64.zip
```

The `.deb` files are emitted when `dpkg-deb` is installed. The release version
must use `MAJOR.MINOR.PATCH` form.

Inspect and install a Debian package locally:

```bash
dpkg-deb --info release/mnc-station_0.1.0_amd64.deb
dpkg-deb --contents release/mnc-station_0.1.0_amd64.deb
sudo apt install ./release/mnc-station_0.1.0_amd64.deb
```

The package installs:

- `/usr/bin/mnc-station`
- `/lib/systemd/system/mnc-station.service`
- `/etc/default/mnc-station`
- `/var/lib/mnc-station`
- `/usr/share/doc/mnc-station`

The service runs as the dedicated `mnc-station` account and receives only
`CAP_NET_BIND_SERVICE`, which is needed for TFTP port 69. Configure the XSDB
path in `/etc/default/mnc-station` after installation.

## Build the Windows MSI

The MSI should be built natively on Windows. Install the .NET SDK first, then
install WiX from PowerShell:

```powershell
dotnet tool install --global wix --version 6.0.0
$env:PATH += ";$env:USERPROFILE\.dotnet\tools"
wix --version
```

If WiX is already installed, use:

```powershell
dotnet tool update --global wix --version 6.0.0
```

Build the Windows executable and MSI:

```powershell
$Version = "0.1.0"
$Commit = (git rev-parse HEAD).Trim()
$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$Ldflags = "-s -w -X main.version=$Version -X main.commit=$Commit -X main.buildDate=$BuildDate"

New-Item -ItemType Directory -Force release\input | Out-Null
go build -trimpath -ldflags $Ldflags `
    -o release\input\mnc-station.exe .\cmd\mnc-station

$SourceDir = (Resolve-Path release\input).Path
wix build -arch x64 `
    -d "Version=$Version" `
    -d "SourceDir=$SourceDir" `
    -o "release\mnc-station_${Version}_windows_amd64.msi" `
    packaging\windows\Product.wxs
```

Install the result on a Windows test machine:

```powershell
msiexec.exe /i "release\mnc-station_0.1.0_windows_amd64.msi"
```

Test installation, upgrade, Start menu launch, uninstallation, XSDB discovery,
and a firewall-approved TFTP transfer on Windows before publishing a release.

## Continuous integration and tagged releases

The CI workflow runs on native Ubuntu and Windows workers. It performs:

- Go tests and vet on both operating systems.
- Formatting and race-detector checks on Linux.
- Linux arm64 and Windows amd64 cross-compilation.

The release workflow is triggered by a strict `vMAJOR.MINOR.PATCH` tag. It
builds Linux archives, Debian packages, a Windows zip, a native Windows MSI,
and SHA-256 checksums before creating the GitHub release.

Before creating a release tag, run locally:

```bash
make check
make test-race
make cross
./packaging/build-release.sh 0.1.0 release
```

Do not create a release tag until the Linux hardware smoke test is complete.

## Verification checklist

Use this checklist for release candidates:

- `gofmt -l .` prints nothing.
- `go vet ./...` succeeds.
- `go test -count=1 ./...` succeeds.
- `go test -count=1 -race ./...` succeeds on Linux.
- Linux amd64 and arm64 binaries compile.
- Windows amd64 compiles and native Windows tests succeed.
- `mnc-station inspect` accepts a real Yocto Station artifact.
- Release archives extract successfully.
- Debian metadata and file ownership are correct.
- MSI install, upgrade, launch, and uninstall succeed on Windows.
- The dashboard loads and reports XSDB availability.
- A separately authorized board smoke test transfers every TFTP payload and
  reaches Linux boot.

## Common build problems

### `go: go.mod requires go >= 1.27`

Install a current Go toolchain and ensure the new `go` binary appears first on
`PATH`. Distribution package repositories may provide an older Go release.

### Race or TFTP tests cannot open a socket

The tests need permission to bind ephemeral loopback TCP and UDP sockets.
Check sandbox, endpoint-security, and firewall policy. They do not require
privileged port 69 and do not contact external hosts.

### `dpkg-deb is required`

Install Debian packaging tools, or use the tar/zip outputs when a `.deb` is not
needed.

### `wix` is not recognized

Add the .NET global-tool directory to `PATH` and open a new PowerShell session:

```powershell
$env:PATH += ";$env:USERPROFILE\.dotnet\tools"
```

### Windows builds but XSDB is unavailable

Compilation does not install Xilinx software. Install Vivado or Vitis on the
Station computer and set `MNC_XSDB` to `xsdb.bat` before running the agent.
