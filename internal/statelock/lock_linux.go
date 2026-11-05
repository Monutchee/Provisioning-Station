//go:build linux

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package statelock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquire(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

func openLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("daemon lock must be a regular file")
	}
	return file, nil
}
