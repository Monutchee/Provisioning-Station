// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package xsdb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func newCommand(ctx context.Context, path string, arguments ...string) *exec.Cmd {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".bat" && extension != ".cmd" {
		return exec.CommandContext(ctx, path, arguments...)
	}
	commandInterpreter := os.Getenv("COMSPEC")
	if commandInterpreter == "" {
		commandInterpreter = "cmd.exe"
	}
	commandArguments := []string{"/d", "/s", "/v:off", "/c", path}
	commandArguments = append(commandArguments, arguments...)
	return exec.CommandContext(ctx, commandInterpreter, commandArguments...)
}
