//go:build !linux && !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package servicecontrol

import (
	"context"
	"fmt"
)

func Start(context.Context) error {
	return fmt.Errorf("machine service management requires Linux or Windows")
}
func Stop(context.Context) error {
	return fmt.Errorf("machine service management requires Linux or Windows")
}
func Query(context.Context) (Status, error) {
	return Status{}, fmt.Errorf("machine service management requires Linux or Windows")
}
