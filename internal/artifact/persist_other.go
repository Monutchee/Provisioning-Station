//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package artifact

import (
	"os"
	"path/filepath"
)

func moveToDeletionStage(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
