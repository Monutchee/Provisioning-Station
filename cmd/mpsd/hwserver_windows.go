//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/Monutchee/Provisioning-Station/internal/daemon"

func executeXilinxHWServer(path string, args []string) error {
	return daemon.HardwareServerCommand(path, args).Run()
}
