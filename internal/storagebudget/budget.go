// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package storagebudget serializes store growth, admission and retention.
// Lock order is Gate -> submissionMutex -> job mutex. Output writers never take
// Gate: their maximum growth is reserved before admission, avoiding deadlocks
// when an import waits for storage while a job completes.
package storagebudget

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const DefaultLimit int64 = 10 << 30

var ErrFull = errors.New("station storage budget exhausted")

type RecoveryLocation struct {
	Path         string `json:"path"`
	Bytes        int64  `json:"bytes"`
	Inaccessible bool   `json:"inaccessible,omitempty"`
}

type Status struct {
	RecoveryLocations []RecoveryLocation `json:"recoveryLocations"`
	LimitBytes        int64              `json:"limitBytes"`
	UsedBytes         int64              `json:"usedBytes"`
	ReservedBytes     int64              `json:"reservedBytes"`
	AvailableBytes    int64              `json:"availableBytes"`
	UnknownBytes      int64              `json:"unknownBytes"`
	OverBudget        bool               `json:"overBudget"`
	CompletedJobLimit int                `json:"completedJobLimit"`
	CompletedJobs     int                `json:"completedJobs"`
	ActiveJobs        int                `json:"activeJobs"`
}

type Gate struct {
	sync.Mutex
	Root         string
	Limit        int64
	Pins         map[string]bool
	Consumption  map[string][]string
	Reservations map[string]int64
	// Reclaim is invoked only under Gate, before growth. It must not acquire Gate.
	Reclaim func(need int64) error
}

func New(root string, limit int64) *Gate {
	return &Gate{Root: root, Limit: limit, Reservations: map[string]int64{}, Pins: map[string]bool{}, Consumption: map[string][]string{}}
}
func Size(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}
func (g *Gate) StatusLocked() (Status, error) {
	s := Status{LimitBytes: g.Limit, CompletedJobLimit: 30}
	remaining := make(map[string]int64, len(g.Reservations))
	paths := map[string]string{}
	for id, n := range g.Reservations {
		remaining[id] = n
		for _, p := range g.Consumption[id] {
			paths[filepath.Clean(p)] = id
		}
	}
	// Derive used bytes and consumed reservation from the SAME per-file stat.
	// Output bypasses Gate. Two separate scans could observe old used bytes but
	// newer consumed credit and incorrectly overstate available capacity.
	err := filepath.WalkDir(g.Root, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		s.UsedBytes += info.Size()
		if id, ok := paths[filepath.Clean(path)]; ok && info.Mode().IsRegular() {
			remaining[id] -= info.Size()
		}
		return nil
	})
	for _, n := range remaining {
		if n > 0 {
			s.ReservedBytes += n
		}
	}
	s.AvailableBytes = g.Limit - s.UsedBytes - s.ReservedBytes
	if s.AvailableBytes < 0 {
		s.AvailableBytes = 0
	}
	s.OverBudget = s.UsedBytes+s.ReservedBytes > g.Limit
	return s, err
}

func (g *Gate) EnsureLocked(n int64) error {
	s, err := g.StatusLocked()
	if err != nil {
		return err
	}
	if n < 0 {
		return fmt.Errorf("negative storage reservation")
	}
	if n <= s.AvailableBytes && !s.OverBudget {
		return nil
	}
	if g.Reclaim != nil {
		if err := g.Reclaim(n); err != nil {
			return err
		}
	}
	s, err = g.StatusLocked()
	if err != nil {
		return err
	}
	if s.OverBudget || n > s.AvailableBytes {
		return ErrFull
	}
	return nil
}
