// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import (
	"errors"
	"os"
)

// OpenArtifactFile rejects nonregular sources and validates the opened handle.
// Nonblocking platform flags prevent a FIFO replacement from hanging the open.
func OpenArtifactFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("artifact must be a regular file")
	}
	file, err := openArtifactFile(path)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("artifact must be a regular file")
	}
	return file, nil
}
