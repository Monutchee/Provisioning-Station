//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"os"
	"path/filepath"
)

func publishMetadata(temporary, path string) error {
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	// Persist both the record and a newly created job directory before execution.
	for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		file, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
