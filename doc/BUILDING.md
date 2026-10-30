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
- Bash, GNU coreutils, `tar`, and `zip` for release archives.
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

Run the release builder on Linux with an output directory:

```bash
./packaging/build-release.sh build/release-auto
```

The script derives its version from reachable canonical `vMAJOR.MINOR.PATCH`
Git tags and the number of commits since the nearest matching tag, using Git's
`describe` selection and distance. A clean exact `v0.1.2` tag produces `0.1.2`.
Two commits after that tag produce `0.1.3-dev2+<commit>`, where `<commit>` is
the first six hexadecimal characters of `HEAD`. This follows the next-patch
and development-distance convention used by MBS, with Station's `-devN`
spelling. The `v` belongs to the Git tag; package names and binary versions omit it.
For example, two commits after `v0.1.2`, with `HEAD` beginning `a1b2c3`, produce:

```text
build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_linux_amd64.tar.gz
build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_linux_arm64.tar.gz
build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_amd64.deb
build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_arm64.deb
build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_windows_amd64.zip
```

The same full version is embedded in every executable. Debian control metadata
replaces `-dev` with `~dev`, for example `0.1.3~dev2+a1b2c3`, so `dpkg` sorts
development packages before `0.1.3`. The `.deb` filename retains `-dev`.

Tracked changes append `.dirty` to the version metadata. A dirty exact tag
becomes the next patch with `dev0`, for example `0.1.3-dev0+a1b2c3.dirty`.
Untracked files do not affect the version, matching MBS's SCM convention.
Without any reachable canonical release tags, the fallback is
`0.1.0-dev<count>+<commit>`, counting all commits reachable from `HEAD`.
Automatic mode rejects shallow history before cleaning output; fetch full
history and tags with `git fetch --unshallow --tags` first. It also rejects
`MNC_STATION_BUILD_ID`; unset that variable to use the Git-derived version.

The `.deb` files are emitted when `dpkg-deb` is installed. The output directory
is the final argument: using `release` writes to `release/`, and using
`build/release` writes to `build/release/`, both under the repository root.

The legacy two-argument form remains available for explicit version builds.
`VERSION` accepts `MAJOR.MINOR.PATCH`, optionally prefixed with `v`. With a `MNC_STATION_BUILD_ID` of 6–40 hexadecimal characters,
it produces `VERSION+<build-id>`; without the override it retains the previous
`VERSION-dev+<commit>` behavior. For example, an explicit version build can
supply a base version and build ID:

```bash
MNC_STATION_BUILD_ID=a1b2c3 \
  ./packaging/build-release.sh 0.1.2 release
```

Six hexadecimal digits identify a commit prefix, not build order. For an apt
feed, provide an increasing numeric UTC identifier in legacy mode when newer
builds of the same base version must sort after older builds:

```bash
MNC_STATION_BUILD_ID="$(date -u +%Y%m%d%H%M%S)" \
  ./packaging/build-release.sh 0.1.2 release
```

Before building, the script removes and recreates the output directory. For
safety, that directory must be inside the repository, must not be a symlink,
and must not contain Git-tracked files. Relative output paths are resolved from
the repository root, even when the script is launched from another directory.

The Windows ZIP is cross-compiled on Linux with `GOOS=windows` and contains
`mnc-station.exe`. It does not run native Windows tests or build the MSI; those
steps run in the Windows GitHub Actions jobs.

Inspect and install a Debian package locally:

```bash
dpkg-deb --info build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_amd64.deb
dpkg-deb --contents build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_amd64.deb
sudo apt install ./build/release-auto/mnc-station_0.1.3-dev2+a1b2c3_amd64.deb
```

The package installs:

- `/usr/bin/mnc-station`
- `/usr/share/bash-completion/completions/mnc-station`
- `/lib/systemd/system/mnc-station.service`
- `/lib/systemd/system/mnc-station-hw-server.service`
- `/etc/default/mnc-station`
- `/var/lib/mnc-station`
- `/usr/share/doc/mnc-station`

The Station and local Xilinx hardware-server services run as the dedicated
`mnc-station` account, whose home is `/var/lib/mnc-station`. The Station service
receives only `CAP_NET_BIND_SERVICE`, which is needed for TFTP port 69. The
post-install script also adds that account to `dialout` when available so it
can open FTDI tty devices, and to `plugdev` when available for group-accessible
JTAG devices. Xilinx's cable-driver udev rules remain responsible for granting
access to the JTAG adapter.

## Build the Windows MSI

The MSI should be built natively on Windows. Install the .NET SDK first, then
install WiX from PowerShell:

```powershell
dotnet tool install --global wix --version 6.0.0
$env:PATH += ";$env:USERPROFILE\.dotnet\tools"
wix --version
```

The installed directory contains the application and third-party license
notices alongside the executable.

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
Copy-Item -Path LICENSE, THIRD_PARTY_NOTICES.md -Destination release\input\

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

The Linux CI job builds and uploads automatic packages as the `station-packages`
Actions artifact. Its five package filenames match a local automatic build of
the same clean Git commit with the same tag history. Pull-request CI may check
out GitHub's merge commit, which has its own commit hash and distance. Legacy
`0.0.0+012345` packages are tested separately and are not uploaded. Extracted
verification files are also excluded from the upload.

The release workflow is triggered by a strict `vMAJOR.MINOR.PATCH` tag. It
uses the same Git-version helper and automatic build command as local builds.
An exact clean tag produces `MAJOR.MINOR.PATCH` in every package filename and
executable, including the native Windows MSI. It builds Linux archives, Debian
packages, a Windows zip, the MSI, and SHA-256 checksums, then creates a GitHub
release. The workflow artifacts are temporary copies; the files attached to the GitHub
release are the permanent release downloads.

Before creating a release tag, run locally:

```bash
make check
make test-race
make cross
./packaging/build-release.sh build/release-auto
```

Do not create a release tag until the Linux hardware smoke test is complete.

Create and push an annotated tag when the commit is ready:

```bash
git tag -a v0.1.0 -m "Provisioning Station v0.1.0"
git push origin v0.1.0
```

The tagged commit must contain `.github/workflows/release.yml`. After the tag
is pushed, open the repository's **Actions** page to follow the `Release`
workflow. When all jobs pass, the repository's **Releases** page contains:

```text
mnc-station_0.1.0_linux_amd64.tar.gz
mnc-station_0.1.0_linux_arm64.tar.gz
mnc-station_0.1.0_amd64.deb
mnc-station_0.1.0_arm64.deb
mnc-station_0.1.0_windows_amd64.zip
mnc-station_0.1.0_windows_amd64.msi
SHA256SUMS
```

The executable inside the MSI and its internal Windows Installer version both
report `0.1.0`. Windows Installer requires three numeric product-version fields.

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
- `bash packaging/test-git-version.sh` passes synthetic Git-history cases.
- Debian metadata and file ownership are correct.
- The Debian hardware-server unit resolves the installed Xilinx `hw_server`
  and listens only on loopback by default.
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

### `dpkg` reports that a newer local build is a downgrade

Six-character Git hexadecimal IDs identify content but do not encode
build order. Debian compares the complete version string, so for example
`0.0.1+08b762` sorts before `0.0.1+bf7649` regardless of which package was
built later. This warning does not mean the package is corrupt.

Use a numeric UTC build ID when packages with the same base version must sort
in build order:

```bash
MNC_STATION_BUILD_ID="$(date -u +%Y%m%d%H%M%S)" \
  ./packaging/build-release.sh 0.0.1 release
```

### `wix` is not recognized

Add the .NET global-tool directory to `PATH` and open a new PowerShell session:

```powershell
$env:PATH += ";$env:USERPROFILE\.dotnet\tools"
```

### Windows builds but XSDB is unavailable

Compilation does not install Xilinx software. Install Vivado, Vitis, or the
standalone Hardware Server package on the Station computer. The agent searches
`PATH` and `C:\Xilinx`; set `MNC_XSDB` to the full `xsdb.bat` path only for a
nonstandard installation or explicit version selection.
