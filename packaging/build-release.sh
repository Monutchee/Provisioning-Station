#!/usr/bin/env bash

set -Eeuo pipefail

if (($# != 2)); then
    printf 'Usage: %s VERSION OUTPUT_DIR\n' "$0" >&2
    printf 'Set MNC_STATION_BUILD_ID to override the generated build ID.\n' >&2
    exit 2
fi

VERSION="${1#v}"
OUTPUT_REQUEST="$2"
[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    printf 'Release version must use MAJOR.MINOR.PATCH: %s\n' "${VERSION}" >&2
    exit 2
}
PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

BUILD_ID="${MNC_STATION_BUILD_ID:-}"
if [[ -z "${BUILD_ID}" ]]; then
    BUILD_ID="$(od -An -N3 -tx1 /dev/urandom | tr -d '[:space:]')"
fi
BUILD_ID="${BUILD_ID,,}"
[[ "${BUILD_ID}" =~ ^[0-9a-f]{6,40}$ ]] || {
    printf 'Build ID must contain 6 to 40 hexadecimal characters: %s\n' \
        "${BUILD_ID}" >&2
    exit 2
}
RELEASE_VERSION="${VERSION}+${BUILD_ID}"

[[ -n "${OUTPUT_REQUEST}" && "${OUTPUT_REQUEST}" != *$'\n'* && \
   "${OUTPUT_REQUEST}" != *$'\r'* ]] || {
    printf 'Output directory must be a non-empty single-line path\n' >&2
    exit 2
}
if [[ "${OUTPUT_REQUEST}" == /* ]]; then
    OUTPUT_CANDIDATE="${OUTPUT_REQUEST%/}"
else
    OUTPUT_CANDIDATE="${PROJECT_ROOT}/${OUTPUT_REQUEST%/}"
fi
[[ -n "${OUTPUT_CANDIDATE}" ]] || {
    printf 'Refusing the filesystem root as the output directory\n' >&2
    exit 2
}
[[ ! -L "${OUTPUT_CANDIDATE}" ]] || {
    printf 'Refusing symlink output directory: %s\n' "${OUTPUT_CANDIDATE}" >&2
    exit 2
}
OUTPUT_DIR="$(realpath -m -- "${OUTPUT_CANDIDATE}")"
case "${OUTPUT_DIR}" in
    "${PROJECT_ROOT}"/*) ;;
    *)
        printf 'Output directory must be inside %s: %s\n' \
            "${PROJECT_ROOT}" "${OUTPUT_DIR}" >&2
        exit 2
        ;;
esac
OUTPUT_RELATIVE="${OUTPUT_DIR#"${PROJECT_ROOT}/"}"
case "${OUTPUT_RELATIVE}" in
    .git|.git/*)
        printf 'Refusing Git metadata as the output directory: %s\n' \
            "${OUTPUT_DIR}" >&2
        exit 2
        ;;
esac
if [[ -n "$(git -C "${PROJECT_ROOT}" ls-files -- "${OUTPUT_RELATIVE}")" ]]; then
    printf 'Refusing output directory containing tracked files: %s\n' \
        "${OUTPUT_DIR}" >&2
    exit 2
fi
if [[ -e "${OUTPUT_DIR}" && ! -d "${OUTPUT_DIR}" ]]; then
    printf 'Output path exists and is not a directory: %s\n' "${OUTPUT_DIR}" >&2
    exit 2
fi

GO="${GO:-go}"
COMMIT="$(git -C "${PROJECT_ROOT}" rev-parse HEAD)"
BUILD_DATE="${SOURCE_DATE_EPOCH:+$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
LDFLAGS="-s -w -X main.version=${RELEASE_VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"
STAGING="$(mktemp -d)"
trap 'rm -rf -- "${STAGING}"' EXIT

printf 'Release version: %s\n' "${RELEASE_VERSION}"
printf 'Cleaning output directory: %s\n' "${OUTPUT_DIR}"
rm -rf -- "${OUTPUT_DIR}"
mkdir -p -- "${OUTPUT_DIR}"

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
    package="mnc-station_${RELEASE_VERSION}_linux_${arch}"
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
            "${RELEASE_VERSION}" "${arch}" "${root}/mnc-station" "${OUTPUT_DIR}"
    fi
done

package="mnc-station_${RELEASE_VERSION}_windows_amd64"
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
printf 'Release packages: %s\n' "${OUTPUT_DIR}"
