//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
)

func machineDataDir() (string, error) { return "/var/lib/mps", nil }
func runService([]string) error {
	return fmt.Errorf("SCM hosting is available only on Windows; use mpsd serve under systemd")
}
