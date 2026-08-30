// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package xsdb

import (
	"context"
	"os/exec"
)

func newCommand(ctx context.Context, path string, arguments ...string) *exec.Cmd {
	return exec.CommandContext(ctx, path, arguments...)
}
