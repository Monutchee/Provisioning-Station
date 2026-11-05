// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package servicecontrol controls the installed machine service without prompts
// or privilege escalation. Starting a service is separate from API readiness.
package servicecontrol

import "fmt"

const Name = "mps"

type Status struct {
	State string `json:"state"`
}

func actionError(action string, err error) error {
	return fmt.Errorf("%s machine service: %w; use an administrator terminal to manage %s", action, err, Name)
}
