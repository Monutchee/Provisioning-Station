//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import "os"

func openRegular(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
