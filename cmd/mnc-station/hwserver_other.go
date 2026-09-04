// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"syscall"
)

func executeXilinxHWServer(path string, arguments []string) error {
	return syscall.Exec(path, append([]string{path}, arguments...), os.Environ())
}
