//go:build linux || windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package statelock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExclusiveAndReleased(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(dir); err == nil {
		second.Close()
		t.Fatal("competing lock succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}
func TestProcessExitReleasesLock(t *testing.T) {
	if dir := os.Getenv("MPS_LOCK_TEST_DIR"); dir != "" {
		lock, err := Acquire(dir)
		if err != nil {
			os.Exit(2)
		}
		_ = lock
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessExitReleasesLock$")
	cmd.Env = append(os.Environ(), "MPS_LOCK_TEST_DIR="+dir)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
}

func TestRejectSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "daemon.lock")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if lock, err := Acquire(dir); err == nil {
		lock.Close()
		t.Fatal("accepted symlink lock")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserved" {
		t.Fatalf("lock target changed: %q, %v", data, err)
	}
}
