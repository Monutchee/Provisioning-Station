//go:build !linux && !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package statelock

import (
	"fmt"
	"os"
)

func acquire(file *os.File) error {
	return fmt.Errorf("daemon state locking requires Linux or Windows")
}

func openLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("daemon state locking requires Linux or Windows")
}
