#!/usr/bin/env bash

set -Eeuo pipefail

if (($# != 5)); then
    printf 'Usage: %s VERSION ARCH CLIENT_BINARY DAEMON_BINARY OUTPUT_DIR\n' "$0" >&2
    exit 2
fi

VERSION="${1#v}"
ARCH="$2"
BINARY="$3"
DAEMON_BINARY="$4"
OUTPUT_DIR="$5"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd -P)"

[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.+~-][A-Za-z0-9.+-]+)?$ ]] || {
    printf 'Invalid Debian package version: %s\n' "${VERSION}" >&2
    exit 2
}
# Debian uses ~ so development packages sort before the final release.
DEBIAN_VERSION="${VERSION/-dev/~dev}"
case "${ARCH}" in
    amd64|arm64) ;;
    *) printf 'Unsupported Debian architecture: %s\n' "${ARCH}" >&2; exit 2 ;;
esac
[[ -f "${BINARY}" ]] || { printf 'Missing binary: %s\n' "${BINARY}" >&2; exit 2; }
[[ -f "${DAEMON_BINARY}" ]] || { printf 'Missing daemon binary: %s\n' "${DAEMON_BINARY}" >&2; exit 2; }
command -v dpkg-deb >/dev/null 2>&1 || { printf 'dpkg-deb is required\n' >&2; exit 2; }

STAGING="$(mktemp -d)"
trap 'rm -rf -- "${STAGING}"' EXIT
PACKAGE_ROOT="${STAGING}/mps"
install -d -m 0755 \
    "${PACKAGE_ROOT}/DEBIAN" \
    "${PACKAGE_ROOT}/usr/bin" \
    "${PACKAGE_ROOT}/usr/share/doc/mps" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/api" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/doc" \
    "${PACKAGE_ROOT}/usr/share/bash-completion/completions" \
    "${PACKAGE_ROOT}/lib/systemd/system" \
    "${PACKAGE_ROOT}/etc/default" \
    "${PACKAGE_ROOT}/var/lib/mps"
chmod 0700 "${PACKAGE_ROOT}/var/lib/mps"
install -m 0755 "${BINARY}" "${PACKAGE_ROOT}/usr/bin/mps"
install -m 0755 "${DAEMON_BINARY}" "${PACKAGE_ROOT}/usr/bin/mpsd"
install -m 0644 "${PROJECT_ROOT}/LICENSE" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/LICENSE"
install -m 0644 "${PROJECT_ROOT}/THIRD_PARTY_NOTICES.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/THIRD_PARTY_NOTICES.md"
install -m 0644 "${PROJECT_ROOT}/README.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/README.md"
install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/doc/README.md"
install -m 0644 "${PROJECT_ROOT}/doc/BUILDING.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/doc/BUILDING.md"
install -m 0644 "${PROJECT_ROOT}/doc/DEPLOYMENT_TASKS.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/doc/DEPLOYMENT_TASKS.md"
install -m 0644 "${PROJECT_ROOT}/doc/MIGRATION.md" "${PACKAGE_ROOT}/usr/share/doc/mps/doc/MIGRATION.md"
install -m 0644 "${PROJECT_ROOT}/doc/SERIAL_CONSOLE.md" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/doc/SERIAL_CONSOLE.md"
install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" \
    "${PACKAGE_ROOT}/usr/share/doc/mps/api/openapi.yaml"
install -m 0644 "${PROJECT_ROOT}/packaging/bash-completion/mps" \
    "${PACKAGE_ROOT}/usr/share/bash-completion/completions/mps"
install -m 0644 "${PROJECT_ROOT}/packaging/bash-completion/mpsd" \
    "${PACKAGE_ROOT}/usr/share/bash-completion/completions/mpsd"
install -m 0644 "${PROJECT_ROOT}/packaging/systemd/mps.service" \
    "${PACKAGE_ROOT}/lib/systemd/system/mps.service"
install -m 0644 "${PROJECT_ROOT}/packaging/systemd/mps-hw-server.service" \
    "${PACKAGE_ROOT}/lib/systemd/system/mps-hw-server.service"
install -m 0644 "${PROJECT_ROOT}/packaging/systemd/mps.default" \
    "${PACKAGE_ROOT}/etc/default/mps"

printf '/etc/default/mps\n' > "${PACKAGE_ROOT}/DEBIAN/conffiles"

cat > "${PACKAGE_ROOT}/DEBIAN/control" <<EOF
Package: mps
Version: ${DEBIAN_VERSION}
Section: utils
Priority: optional
Architecture: ${ARCH}
Maintainer: Monutchee maintainers <21245380+lesterlo@users.noreply.github.com>
Homepage: https://github.com/Monutchee/Provisioning-Station
Depends: adduser, init-system-helpers
Conflicts: mnc-station
Suggests: bash-completion
Description: Local Monutchee hardware provisioning agent
 Validates Station artifacts and performs managed Xilinx JTAG/TFTP boot jobs.
EOF

cat > "${PACKAGE_ROOT}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if ! getent group mps >/dev/null; then
    addgroup --system mps >/dev/null
fi
if ! getent passwd mps >/dev/null; then
    adduser --system --ingroup mps --home /var/lib/mps \
        --no-create-home --disabled-login mps >/dev/null
fi
if getent group dialout >/dev/null; then
    adduser mps dialout >/dev/null 2>&1 || true
fi
if getent group plugdev >/dev/null; then
    adduser mps plugdev >/dev/null 2>&1 || true
fi
install -d -m 0700 -o mps -g mps /var/lib/mps
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl enable mps.service >/dev/null 2>&1 || true
    if [ ! -e /var/lib/mps/.migration-in-progress ]; then
        deb-systemd-invoke restart mps.service >/dev/null 2>&1 || true
    fi
fi
EOF

cat > "${PACKAGE_ROOT}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = remove ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop mps.service >/dev/null 2>&1 || true
    systemctl stop mps-hw-server.service >/dev/null 2>&1 || true
    systemctl disable mps.service >/dev/null 2>&1 || true
    systemctl disable mps-hw-server.service >/dev/null 2>&1 || true
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
    "${OUTPUT_DIR}/mps_${VERSION}_${ARCH}.deb"
