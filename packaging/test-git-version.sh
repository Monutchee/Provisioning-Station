#!/usr/bin/env bash
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0
set -Eeuo pipefail

PROJECT_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "${PROJECT_ROOT}/packaging/git-version.sh"
SCRATCH="$(mktemp -d)"
trap 'rm -rf -- "$SCRATCH"' EXIT

git_fixture() { git -C "$REPO" "$@"; }
new_repository() {
    REPO="$SCRATCH/$1"
    git init --quiet "$REPO"
    git_fixture config user.name 'Synthetic Test'
    git_fixture config user.email 'test@example.invalid'
    printf 'fixture\n' > "$REPO/tracked"
    git_fixture add tracked
    git_fixture commit --quiet -m fixture
}
assert_display() {
    local actual
    actual="$(station_display_version "$REPO")"
    [[ "$actual" == "$1" ]] || { printf 'Display expected %s, got %s\n' "$1" "$actual" >&2; exit 1; }
}
assert_version() {
    local actual
    actual="$(station_git_version "$REPO")"
    if [[ "$actual" != "$1" ]]; then
        printf 'Expected %s, got %s\n' "$1" "$actual" >&2
        exit 1
    fi
}

new_repository tagged
SHA="$(git_fixture rev-parse HEAD)"
assert_version "0.1.0-dev1+${SHA:0:6}"
assert_display "v0.1.0-1-${SHA:0:12}"
git_fixture tag -a v0.1.2 -m 'Synthetic release'
assert_version '0.1.2'
assert_display v0.1.2
printf 'untracked\n' > "$REPO/untracked"
assert_version '0.1.2'
assert_display v0.1.2
printf 'dirty\n' > "$REPO/tracked"
assert_version "0.1.3-dev0+${SHA:0:6}.dirty"
assert_display "v0.1.2-0-${SHA:0:12}-dirty"
git_fixture add tracked
assert_version "0.1.3-dev0+${SHA:0:6}.dirty"
assert_display "v0.1.2-0-${SHA:0:12}-dirty"
git_fixture commit --quiet -m first
git_fixture commit --quiet --allow-empty -m second
SHA="$(git_fixture rev-parse HEAD)"
assert_version "0.1.3-dev2+${SHA:0:6}"
assert_display "v0.1.2-2-${SHA:0:12}"
# Noncanonical and prerelease tags do not replace the canonical base tag.
git_fixture tag v01.2.3
git_fixture tag v9.9.9-rc1
assert_version "0.1.3-dev2+${SHA:0:6}"
assert_display "v0.1.2-2-${SHA:0:12}"
printf 'dirty again\n' > "$REPO/tracked"
assert_version "0.1.3-dev2+${SHA:0:6}.dirty"
assert_display "v0.1.2-2-${SHA:0:12}-dirty"
git_fixture restore tracked

git clone --quiet --depth 1 "file://$REPO" "$SCRATCH/shallow"
if station_git_version "$SCRATCH/shallow" >"$SCRATCH/stdout" 2>"$SCRATCH/stderr"; then
    printf 'Shallow history was accepted\n' >&2
    exit 1
fi
grep -Fq 'git fetch --unshallow --tags' "$SCRATCH/stderr"
mkdir -p "$SCRATCH/shallow/packaging" "$SCRATCH/shallow/build/output"
cp "$PROJECT_ROOT/packaging/build-release.sh" "$PROJECT_ROOT/packaging/git-version.sh" "$SCRATCH/shallow/packaging/"
touch "$SCRATCH/shallow/build/output/sentinel"
if env -u MPS_BUILD_ID bash "$SCRATCH/shallow/packaging/build-release.sh" build/output >"$SCRATCH/stdout" 2>"$SCRATCH/stderr"; then
    printf 'Shallow packaging was accepted\n' >&2
    exit 1
fi
grep -Fq 'git fetch --unshallow --tags' "$SCRATCH/stderr"
test -f "$SCRATCH/shallow/build/output/sentinel"

new_repository lightweight
git_fixture tag v2.3.9
assert_version '2.3.9'
git_fixture commit --quiet --allow-empty -m next
SHA="$(git_fixture rev-parse HEAD)"
assert_version "2.3.10-dev1+${SHA:0:6}"
printf 'Git version fixtures passed\n'
