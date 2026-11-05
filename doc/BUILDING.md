# Building Provisioning Station

Provisioning Station ships two embedded-asset Go executables: `mpsd` owns the
HTTP API, job state, and hardware; `mps` is the HTTP client, terminal UI, and
stdio MCP server. Both binaries are required for a normal local installation.
Node.js is needed only to maintain and verify the embedded hterm assets.

## Requirements

Use Go 1.27+, Git with full history and tags, and a writable Go build cache.
Linux package builds need `dpkg-deb`, `tar`, and `zip`. Native Windows MSI builds
use .NET and WiX 6. No Xilinx software is needed to compile or run software tests.
Hardware discovery and provisioning require a separate vendor installation.

## Build and test

```bash
make build
./dist/mps version
./dist/mpsd version
make check
make test-race
make cross
bash packaging/test-git-version.sh
python3 -B packaging/migration/test_migrate.py
```

`make build` produces `dist/mps` and `dist/mpsd`. `make cross` produces each for
Linux amd64/arm64 and Windows amd64. All runtime changes must pass `go test
./...`, `go vet ./...`, and Linux/Windows builds before handoff. Windows CI runs Go tests and constructs the MSI. Native service installation,
SCM lifecycle, directory ACLs, and vendor hardware behavior still require
manual Windows validation; cross-builds do not exercise them.
Physical JTAG tests reset hardware and require explicit target authorization.

From Windows PowerShell, build both programs:

```powershell
go test ./...
go vet ./...
go build -trimpath -o dist\mps.exe .\cmd\mps
go build -trimpath -o dist\mpsd.exe .\cmd\mpsd
```

For embedded terminal maintenance and browser tests:

```bash
cd tools/hterm
npm ci --ignore-scripts
npm run check
npx playwright install --with-deps chromium
STATION_BINARY=/absolute/path/to/dist/mpsd npm test
```

## Version identity and package ordering

The display version uses reachable canonical `vMAJOR.MINOR.PATCH` tags:

- Clean exact tag: `v1.2.3`.
- Seven commits after that tag: `v1.2.3-7-abcdef123456`.
- Tracked changes add `-dirty`, including `v1.2.3-0-abcdef123456-dirty`.
- Without a tag: `v0.1.0-<commit-count>-<12-character-commit>`.

Untracked files do not affect the display version. Automatic version resolution
rejects shallow history; fetch full history and tags first. Both binaries use
`internal/buildinfo`, with the same version, full commit, and UTC build timestamp.
A direct `go build` uses Go's embedded VCS revision/time/modified metadata to
report `devel-<commit>[-dirty]`; it cannot infer a tag or commit distance. Linker
metadata takes precedence over that fallback.

Package versions are separate. A clean exact `v1.2.3` uses package version
`1.2.3`. Development builds after it use the next patch,
`1.2.4-dev7+abcdef`, and Debian normalizes `-dev` to `~dev` so those builds sort
before final `1.2.4`. MSI requires a numeric three-component version. Never
compare display strings to decide package upgrade order.

## Build release archives and Debian packages

```bash
./packaging/build-release.sh build/release-auto
```

This derives display and package versions from Git and produces:

```text
mps_<package-version>_linux_amd64.tar.gz
mps_<package-version>_linux_arm64.tar.gz
mps_<package-version>_amd64.deb
mps_<package-version>_arm64.deb
mps_<package-version>_windows_amd64.zip
```

Each archive contains both binaries, licenses, API documentation, and user
guides. Linux archives also include completions for both commands. The script
cleans only its validated output directory inside the source tree. It rejects
tracked output directories, symlink outputs, and Git metadata directories.

The explicit package-version form is also available:

```bash
MPS_BUILD_ID=a1b2c3 ./packaging/build-release.sh 1.2.3 build/release-explicit
```

This selects package version `1.2.3+a1b2c3`; without `MPS_BUILD_ID` it uses
`1.2.3-dev+<commit>`. The displayed version still identifies the actual source
checkout. `MPS_BUILD_ID` is rejected for the automatic one-argument form.

To build a Debian package from existing binaries:

```bash
./packaging/debian/build-deb.sh 1.2.3 amd64 dist/mps dist/mpsd build/deb
```

The package installs `/usr/bin/mps`, `/usr/bin/mpsd`, `mps.service`,
`mps-hw-server.service`, and the conffile `/etc/default/mps`. It creates the
`mps` system account/group and private `/var/lib/mps`. The daemon's unit grants
only the low-port bind capability; vendor tools remain separate executables.
Service starts honor `policy-rc.d`. Package removal stops the services and
retains state/account; it never destroys job or artifact data.

Do not install this package over the legacy package without following the
[migration procedure](MIGRATION.md). They conflict deliberately. The migration
requires the exact legacy `.deb` for rollback.

## Build the Windows MSI

The MSI installs both binaries in Program Files, adds the client to the system
PATH, and installs the native SCM service `mps` running `mpsd.exe service` under
LocalService. It creates `%ProgramData%\Monutchee\MPS` with a protected ACL for
SYSTEM, Administrators, and LocalService. State persists through uninstall.
The existing MSI UpgradeCode remains stable for major upgrades. Previous
per-user state is not inferred by an elevated installer; migrate it explicitly.

From a clean source checkout on Windows:

```powershell
$Version = "1.2.3" # MSI numeric version
$DisplayVersion = "v1.2.3"
$Commit = (git rev-parse HEAD).Trim()
$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$Info = "github.com/Monutchee/Provisioning-Station/internal/buildinfo"
$Ldflags = "-s -w -X $Info.Version=$DisplayVersion -X $Info.Commit=$Commit -X $Info.BuildDate=$BuildDate"
New-Item -ItemType Directory -Force release\input | Out-Null
go build -trimpath -ldflags $Ldflags -o release\input\mps.exe .\cmd\mps
go build -trimpath -ldflags $Ldflags -o release\input\mpsd.exe .\cmd\mpsd
Copy-Item LICENSE, THIRD_PARTY_NOTICES.md release\input\
dotnet tool install --global wix --version 6.0.0
wix build -arch x64 -d Version=$Version -d SourceDir="$pwd\release\input" -o release\mps.msi packaging\windows\Product.wxs
```

Validate install/start/stop/upgrade/uninstall and ACL behavior on Windows.
Linux XML parsing or Go cross-compilation does not prove MSI behavior.

## Continuous integration and tagged releases

CI tests and vets both binaries on Linux and Windows, runs race checks,
cross-builds both programs, verifies package contents/versioning, authors the
MSI on Windows, and checks embedded terminal assets/browser behavior.
Tagged `vMAJOR.MINOR.PATCH` releases require a clean exact tag and full Git
history. Release artifacts use package versions in filenames, carry the SCM
display version in both binaries, and include checksums.
