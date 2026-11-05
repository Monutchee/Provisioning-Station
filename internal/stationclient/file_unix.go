//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import (
	"os"
	"syscall"
)

func openArtifactFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
