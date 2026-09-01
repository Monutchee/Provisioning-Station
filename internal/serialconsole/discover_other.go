// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package serialconsole

import "fmt"

func nativeCandidates() ([]candidate, error) {
	return nil, fmt.Errorf("serial console discovery is supported only on Linux and Windows")
}
