#!/usr/bin/env bash

set -Eeuo pipefail

if (($# != 1 && $# != 2)); then
    printf 'Usage: %s [VERSION] OUTPUT_DIR\n' "$0" >&2
    printf 'With OUTPUT_DIR only, derive the version from Git tags and commit distance.\n' >&2
    printf 'Legacy VERSION accepts MPS_BUILD_ID for VERSION+<build-id>.\n' >&2
    exit 2
fi

PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
COMMIT="$(git -C "${PROJECT_ROOT}" rev-parse HEAD)"
source "${PROJECT_ROOT}/packaging/git-version.sh"
DISPLAY_VERSION="$(station_display_version "${PROJECT_ROOT}")"
if (($# == 1)); then
    OUTPUT_REQUEST="$1"
    if [[ -n "${MPS_BUILD_ID:-}" ]]; then
        printf 'MPS_BUILD_ID requires the legacy VERSION OUTPUT_DIR form.\n' >&2
        exit 2
    fi
    source "${PROJECT_ROOT}/packaging/git-version.sh"
    RELEASE_VERSION="$(station_git_version "${PROJECT_ROOT}")"
else
    VERSION="${1#v}"
    OUTPUT_REQUEST="$2"
    [[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
        printf 'Release version must use MAJOR.MINOR.PATCH: %s\n' "${VERSION}" >&2
        exit 2
    }
    BUILD_ID="${MPS_BUILD_ID:-}"
    VERSION_SUFFIX=""
    if [[ -z "${BUILD_ID}" ]]; then
        BUILD_ID="${COMMIT:0:6}"
        VERSION_SUFFIX="-dev"
    fi
    BUILD_ID="${BUILD_ID,,}"
    [[ "${BUILD_ID}" =~ ^[0-9a-f]{6,40}$ ]] || {
        printf 'Build ID must contain 6 to 40 hexadecimal characters: %s\n' "${BUILD_ID}" >&2
        exit 2
    }
    RELEASE_VERSION="${VERSION}${VERSION_SUFFIX}+${BUILD_ID}"
fi

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
BUILD_DATE="${SOURCE_DATE_EPOCH:+$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILDINFO="github.com/Monutchee/Provisioning-Station/internal/buildinfo"
LDFLAGS="-s -w -X ${BUILDINFO}.Version=${DISPLAY_VERSION} -X ${BUILDINFO}.Commit=${COMMIT} -X ${BUILDINFO}.BuildDate=${BUILD_DATE}"
STAGING="$(mktemp -d)"
trap 'rm -rf -- "${STAGING}"' EXIT

printf 'Release version: %s\n' "${RELEASE_VERSION}"
printf 'Cleaning output directory: %s\n' "${OUTPUT_DIR}"
rm -rf -- "${OUTPUT_DIR}"
mkdir -p -- "${OUTPUT_DIR}"

build_binary() {
    local goos="$1" goarch="$2" output="$3" command="$4"
    (
        cd -- "${PROJECT_ROOT}"
        CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
            "${GO}" build -trimpath -ldflags "${LDFLAGS}" \
            -o "${output}" "./cmd/${command}"
    )
}

for arch in amd64 arm64; do
    package="mps_${RELEASE_VERSION}_linux_${arch}"
    root="${STAGING}/${package}"
    mkdir -p -- "${root}"
    build_binary linux "${arch}" "${root}/mps" mps
    build_binary linux "${arch}" "${root}/mpsd" mpsd
    install -m 0644 "${PROJECT_ROOT}/LICENSE" "${PROJECT_ROOT}/README.md" \
        "${PROJECT_ROOT}/THIRD_PARTY_NOTICES.md" "${root}/"
    install -d -m 0755 "${root}/doc"
    install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
        "${PROJECT_ROOT}/doc/BUILDING.md" \
        "${PROJECT_ROOT}/doc/DEPLOYMENT_TASKS.md" \
        "${PROJECT_ROOT}/doc/SERIAL_CONSOLE.md" "${PROJECT_ROOT}/doc/MIGRATION.md" "${root}/doc/"
    install -d -m 0755 "${root}/api"
    install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" "${root}/api/"
    install -d -m 0755 "${root}/completion"
    install -m 0644 "${PROJECT_ROOT}/packaging/bash-completion/mps" \
        "${root}/completion/mps.bash"
    install -m 0644 "${PROJECT_ROOT}/packaging/bash-completion/mpsd" "${root}/completion/mpsd.bash"
    install -d -m 0755 "${root}/migration/legacy-units"
    install -m 0755 "${PROJECT_ROOT}/packaging/migration/migrate.py" "${root}/migration/"
    install -m 0644 "${PROJECT_ROOT}/packaging/migration/legacy-units/"*.service "${root}/migration/legacy-units/"
    tar -C "${STAGING}" -czf "${OUTPUT_DIR}/${package}.tar.gz" "${package}"
    if command -v dpkg-deb >/dev/null 2>&1; then
        "${PROJECT_ROOT}/packaging/debian/build-deb.sh" \
            "${RELEASE_VERSION}" "${arch}" "${root}/mps" "${root}/mpsd" "${OUTPUT_DIR}"
    fi
done

package="mps_${RELEASE_VERSION}_windows_amd64"
root="${STAGING}/${package}"
mkdir -p -- "${root}"
build_binary windows amd64 "${root}/mps.exe" mps
build_binary windows amd64 "${root}/mpsd.exe" mpsd
install -m 0644 "${PROJECT_ROOT}/LICENSE" "${PROJECT_ROOT}/README.md" \
    "${PROJECT_ROOT}/THIRD_PARTY_NOTICES.md" "${root}/"
install -d -m 0755 "${root}/doc"
install -m 0644 "${PROJECT_ROOT}/doc/README.md" \
    "${PROJECT_ROOT}/doc/BUILDING.md" \
    "${PROJECT_ROOT}/doc/DEPLOYMENT_TASKS.md" \
    "${PROJECT_ROOT}/doc/SERIAL_CONSOLE.md" "${PROJECT_ROOT}/doc/MIGRATION.md" "${root}/doc/"
install -d -m 0755 "${root}/api"
install -m 0644 "${PROJECT_ROOT}/api/openapi.yaml" "${root}/api/"
(
    cd "${STAGING}"
    zip -qr "${OUTPUT_DIR}/${package}.zip" "${package}"
)
printf 'Release packages: %s\n' "${OUTPUT_DIR}"
