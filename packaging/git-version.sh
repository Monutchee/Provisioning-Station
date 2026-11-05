#!/usr/bin/env bash
# Copyright 2026 Monutchee
# SPDX-License-Identifier: Apache-2.0

# Resolve from an explicit source tree, never the caller's working directory.
station_git_version() {
    local root="$1" commit tag description distance base major minor patch dirty=""
    local canonical='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
    local -a matches=()
    commit="$(git -C "$root" rev-parse --verify HEAD)" || return
    if [[ "$(git -C "$root" rev-parse --is-shallow-repository)" == true ]]; then
        printf 'Automatic versioning requires full Git history and tags; run git fetch --unshallow --tags.\n' >&2
        return 2
    fi
    while IFS= read -r tag; do
        if [[ "$tag" =~ $canonical ]]; then
            matches+=(--match "$tag")
        fi
    done < <(git -C "$root" tag --list --merged HEAD)
    if ! git -C "$root" diff --quiet HEAD --; then
        dirty=".dirty"
    fi
    if ((${#matches[@]})); then
        description="$(git -C "$root" describe --tags --long --abbrev=40 "${matches[@]}" HEAD)" || return
        tag="${description%-*-*}"
        distance="${description#"$tag"-}"
        distance="${distance%%-*}"
        base="${tag#v}"
        if [[ "$distance" == 0 && -z "$dirty" ]]; then
            printf '%s\n' "$base"
            return
        fi
        IFS=. read -r major minor patch <<< "$base"
        # Bash arithmetic is bounded; do not silently wrap an unusually large tag.
        if ((${#patch} > 15)); then
            printf 'Release tag patch component is too large to increment: %s\n' "$tag" >&2
            return 2
        fi
        base="${major}.${minor}.$((patch + 1))"
    else
        base="0.1.0"
        distance="$(git -C "$root" rev-list --count HEAD)" || return
    fi
    printf '%s-dev%s+%s%s\n' "$base" "$distance" "${commit:0:6}" "$dirty"
}

# Human-facing SCM identity; package ordering is handled separately above.
station_display_version() {
    local root="$1" commit tag description distance dirty=""
    local canonical='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
    local -a matches=()
    commit="$(git -C "$root" rev-parse --verify HEAD)" || return
    if [[ "$(git -C "$root" rev-parse --is-shallow-repository)" == true ]]; then
        printf 'Automatic versioning requires full Git history and tags; run git fetch --unshallow --tags.\n' >&2
        return 2
    fi
    while IFS= read -r tag; do
        [[ "$tag" =~ $canonical ]] && matches+=(--match "$tag")
    done < <(git -C "$root" tag --list --merged HEAD)
    git -C "$root" diff --quiet HEAD -- || dirty="-dirty"
    if ((${#matches[@]})); then
        description="$(git -C "$root" describe --tags --long --abbrev=12 "${matches[@]}" HEAD)" || return
        tag="${description%-*-*}"
        distance="${description#"$tag"-}"
        distance="${distance%%-*}"
        if [[ "$distance" == 0 && -z "$dirty" ]]; then printf '%s\n' "$tag"; return; fi
    else
        tag=v0.1.0
        distance="$(git -C "$root" rev-list --count HEAD)" || return
    fi
    printf '%s-%s-%s%s\n' "$tag" "$distance" "${commit:0:12}" "$dirty"
}
