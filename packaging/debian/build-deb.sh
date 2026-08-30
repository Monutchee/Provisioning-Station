#!/usr/bin/env bash

set -Eeuo pipefail

if (($# != 4)); then
    printf 'Usage: %s VERSION ARCH BINARY OUTPUT_DIR\n' "$0" >&2
    exit 2
fi

VERSION="${1#v}"
ARCH="$2"
BINARY="$3"
OUTPUT_DIR="$4"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd -P)"

[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.+~-][A-Za-z0-9.-]+)?$ ]] || {
    printf 'Invalid Debian package version: %s\n' "${VERSION}" >&2
    exit 2
}
case "${ARCH}" in
    amd64|arm64) ;;
    *) printf 'Unsupported Debian architecture: %s\n' "${ARCH}" >&2; exit 2 ;;
esac
[[ -f "${BINARY}" ]] || { printf 'Missing binary: %s\n' "${BINARY}" >&2; exit 2; }
command -v dpkg-deb >/dev/null 2>&1 || { printf 'dpkg-deb is required\n' >&2; exit 2; }

STAGING="$(mktemp -d)"
trap 'rm -rf -- "${STAGING}"' EXIT
PACKAGE_ROOT="${STAGING}/mnc-station"
install -d -m 0755 \
    "${PACKAGE_ROOT}/DEBIAN" \
    "${PACKAGE_ROOT}/usr/bin" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/api" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/doc" \
    "${PACKAGE_ROOT}/lib/systemd/system" \
    "${PACKAGE_ROOT}/etc/default" \
    "${PACKAGE_ROOT}/var/lib/mnc-station"
install -m 0755 "${BINARY}" "${PACKAGE_ROOT}/usr/bin/mnc-station"
install -m 0644 "${PROJECT_ROOT}/LICENSE" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/LICENSE"
install -m 0644 "${PROJECT_ROOT}/README.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/README.md"
install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/doc/README.md"
install -m 0644 "${PROJECT_ROOT}/doc/BUILDING.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/doc/BUILDING.md"
install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" \
    "${PACKAGE_ROOT}/usr/share/doc/mnc-station/api/openapi.yaml"
install -m 0644 "${PROJECT_ROOT}/packaging/systemd/mnc-station.service" \
    "${PACKAGE_ROOT}/lib/systemd/system/mnc-station.service"
install -m 0644 "${PROJECT_ROOT}/packaging/systemd/mnc-station.default" \
    "${PACKAGE_ROOT}/etc/default/mnc-station"

cat > "${PACKAGE_ROOT}/DEBIAN/control" <<EOF
Package: mnc-station
Version: ${VERSION}
Section: utils
Priority: optional
Architecture: ${ARCH}
Maintainer: Monutchee maintainers <21245380+lesterlo@users.noreply.github.com>
Homepage: https://github.com/Monutchee/Provisioning-Station
Depends: adduser
Description: Local Monutchee hardware provisioning agent
 Validates Station artifacts and performs managed Xilinx JTAG/TFTP boot jobs.
EOF

cat > "${PACKAGE_ROOT}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if ! getent group mnc-station >/dev/null; then
    addgroup --system mnc-station >/dev/null
fi
if ! getent passwd mnc-station >/dev/null; then
    adduser --system --ingroup mnc-station --home /var/lib/mnc-station \
        --no-create-home --disabled-login mnc-station >/dev/null
fi
install -d -m 0700 -o mnc-station -g mnc-station /var/lib/mnc-station
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl enable mnc-station.service >/dev/null 2>&1 || true
    systemctl restart mnc-station.service >/dev/null 2>&1 || true
fi
EOF

cat > "${PACKAGE_ROOT}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = remove ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop mnc-station.service >/dev/null 2>&1 || true
    systemctl disable mnc-station.service >/dev/null 2>&1 || true
fi
EOF

cat > "${PACKAGE_ROOT}/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi
EOF

chmod 0755 "${PACKAGE_ROOT}/DEBIAN/postinst" \
    "${PACKAGE_ROOT}/DEBIAN/prerm" "${PACKAGE_ROOT}/DEBIAN/postrm"
mkdir -p -- "${OUTPUT_DIR}"
dpkg-deb --root-owner-group --build "${PACKAGE_ROOT}" \
    "${OUTPUT_DIR}/mnc-station_${VERSION}_${ARCH}.deb"
