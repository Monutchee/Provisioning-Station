#!/usr/bin/env bash

set -Eeuo pipefail

if (($# != 2)); then
    printf 'Usage: %s VERSION OUTPUT_DIR\n' "$0" >&2
    exit 2
fi

VERSION="${1#v}"
OUTPUT_DIR="$2"
[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    printf 'Release version must use MAJOR.MINOR.PATCH: %s\n' "${VERSION}" >&2
    exit 2
}
PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
GO="${GO:-go}"
COMMIT="$(git -C "${PROJECT_ROOT}" rev-parse HEAD)"
BUILD_DATE="${SOURCE_DATE_EPOCH:+$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"
STAGING="$(mktemp -d)"
trap 'rm -rf -- "${STAGING}"' EXIT
mkdir -p -- "${OUTPUT_DIR}"
OUTPUT_DIR="$(cd -- "${OUTPUT_DIR}" && pwd -P)"

build_binary() {
    local goos="$1" goarch="$2" output="$3"
    (
        cd -- "${PROJECT_ROOT}"
        CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
            "${GO}" build -trimpath -ldflags "${LDFLAGS}" \
            -o "${output}" ./cmd/mnc-station
    )
}

for arch in amd64 arm64; do
    package="mnc-station_${VERSION}_linux_${arch}"
    root="${STAGING}/${package}"
    mkdir -p -- "${root}"
    build_binary linux "${arch}" "${root}/mnc-station"
    install -m 0644 "${PROJECT_ROOT}/LICENSE" "${PROJECT_ROOT}/README.md" "${root}/"
    install -d -m 0755 "${root}/doc"
    install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
        "${PROJECT_ROOT}/doc/BUILDING.md" "${root}/doc/"
    install -d -m 0755 "${root}/api"
    install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" "${root}/api/"
    tar -C "${STAGING}" -czf "${OUTPUT_DIR}/${package}.tar.gz" "${package}"
    if command -v dpkg-deb >/dev/null 2>&1; then
        "${PROJECT_ROOT}/packaging/debian/build-deb.sh" \
            "${VERSION}" "${arch}" "${root}/mnc-station" "${OUTPUT_DIR}"
    fi
done

package="mnc-station_${VERSION}_windows_amd64"
root="${STAGING}/${package}"
mkdir -p -- "${root}"
build_binary windows amd64 "${root}/mnc-station.exe"
install -m 0644 "${PROJECT_ROOT}/LICENSE" "${PROJECT_ROOT}/README.md" "${root}/"
install -d -m 0755 "${root}/doc"
install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
    "${PROJECT_ROOT}/doc/BUILDING.md" "${root}/doc/"
install -d -m 0755 "${root}/api"
install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" "${root}/api/"
(
    cd "${STAGING}"
    zip -qr "${OUTPUT_DIR}/${package}.zip" "${package}"
)
