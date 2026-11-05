//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import "os"

func openArtifactFile(path string) (*os.File, error) { return os.Open(path) }
