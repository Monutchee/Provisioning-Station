// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package buildinfo shares release metadata between the client and daemon.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func String(binary string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", binary, Version, Commit, BuildDate)
}

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	revision, timestamp, modified := "", "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			timestamp = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if Commit == "unknown" && revision != "" {
		Commit = revision
	}
	if BuildDate == "unknown" && timestamp != "" {
		BuildDate = timestamp
	}
	if Version == "dev" && revision != "" {
		short := revision
		if len(short) > 12 {
			short = short[:12]
		}
		Version = "devel-" + short
		if modified {
			Version += "-dirty"
		}
	}
}
