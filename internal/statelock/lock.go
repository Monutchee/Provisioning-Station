// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package statelock prevents competing daemons from opening the same state.
package statelock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire holds an operating-system lock until Close or process exit. The lock
// file remains on disk: unlinking it would allow locking a different inode.
func Acquire(directory string) (*Lock, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create daemon state directory: %w", err)
	}
	path := filepath.Join(directory, "daemon.lock")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("daemon lock must be a regular file")
	}
	file, err := openLock(path)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if err := acquire(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("daemon state is already in use or cannot be locked: %w", err)
	}
	return &Lock{file: file}, nil
}
func (lock *Lock) Close() error {
	lock.once.Do(func() { lock.err = lock.file.Close() })
	return lock.err
}
