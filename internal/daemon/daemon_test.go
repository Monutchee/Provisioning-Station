// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/statelock"
)

func TestDaemonReadyShutdownReleasesStateAndRestarts(t *testing.T) {
	dataDir := t.TempDir()
	limits := artifact.DefaultLimits()
	config := Config{Version: "test", HTTPListen: "127.0.0.1:0", TFTPListen: "127.0.0.1:0", DataDir: dataDir, XSDBPath: filepath.Join(dataDir, "missing-xsdb"), JobTimeout: time.Second, MaxCompressedBytes: limits.MaxCompressedBytes, MaxUncompressedBytes: limits.MaxUncompressedBytes, SerialBaud: serialconsole.DefaultBaudRate, MaxConsoleLogBytes: serialconsole.DefaultLogBytes}
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan struct{})
		done := make(chan error, 1)
		go func() { done <- Run(ctx, config, func() { close(ready) }) }()
		select {
		case <-ready:
		case err := <-done:
			cancel()
			t.Fatalf("daemon never became ready: %v", err)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("daemon ready timeout")
		}
		if duplicate, err := statelock.Acquire(dataDir); err == nil {
			duplicate.Close()
			cancel()
			t.Fatal("live daemon did not retain exclusive state lock")
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not stop after cancellation")
		}
		lock, err := statelock.Acquire(dataDir)
		if err != nil {
			t.Fatal("shutdown leaked state lock", err)
		}
		lock.Close()
	}
}
