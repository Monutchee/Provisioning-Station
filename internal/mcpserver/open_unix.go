//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"os"
	"syscall"
)

func openRegular(root *os.Root, path string) (*os.File, error) {
	// Nonblocking open prevents a replaced FIFO from hanging before fstat.
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
