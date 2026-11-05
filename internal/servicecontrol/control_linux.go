//go:build linux

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package servicecontrol

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func command(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--no-ask-password"}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("systemctl: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
func Start(ctx context.Context) error {
	_, err := command(ctx, "start", Name+".service")
	if err != nil {
		return actionError("start (run sudo systemctl start mps.service if authorization is required)", err)
	}
	return nil
}
func Stop(ctx context.Context) error {
	_, err := command(ctx, "stop", Name+".service")
	if err != nil {
		return actionError("stop", err)
	}
	return nil
}
func Query(ctx context.Context) (Status, error) {
	state, err := command(ctx, "show", Name+".service", "--property=ActiveState", "--value")
	return Status{State: state}, err
}
