// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DefaultURL = "http://127.0.0.1:8042"

type ReadinessConfig struct {
	URL string
	// Start controls the already-installed OS service without privilege escalation.
	Start        func(context.Context) error
	Timeout      time.Duration
	PollInterval time.Duration
}

// EnsureReady starts only an absent default local listener. Authentication,
// incompatible services and other failures never trigger service control.
// MCP intentionally does not call this orchestration function.
func EnsureReady(ctx context.Context, client *Client, config ReadinessConfig) error {
	timeout := config.Timeout
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := client.Capabilities(ctx)
	if err == nil {
		return nil
	}
	if !IsConnectionRefused(err) || strings.TrimRight(config.URL, "/") != DefaultURL || config.Start == nil {
		return err
	}
	if err = config.Start(ctx); err != nil {
		return fmt.Errorf("start installed station service: %w", err)
	}
	interval := config.PollInterval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_, err = client.Capabilities(ctx)
		if err == nil {
			return nil
		}
		if !IsConnectionRefused(err) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("station did not become ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
