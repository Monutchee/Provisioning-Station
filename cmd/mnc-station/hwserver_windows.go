// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func executeXilinxHWServer(path string, arguments []string) error {
	var command *exec.Cmd
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".bat" || extension == ".cmd" {
		interpreter := os.Getenv("COMSPEC")
		if interpreter == "" {
			interpreter = "cmd.exe"
		}
		commandArguments := []string{"/d", "/s", "/v:off", "/c", path}
		command = exec.Command(interpreter, append(commandArguments, arguments...)...)
	} else {
		command = exec.Command(path, arguments...)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
