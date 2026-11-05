//go:build !windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package daemon

import "context"

func startOwnedHWServer(context.Context) (func(), error) { return func() {}, nil }
