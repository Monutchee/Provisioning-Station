// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package storagebudget

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputReservationAndUsedBytesShareOneSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const total = 1 << 20
	g := New(root, total)
	g.Reservations["job"] = total
	g.Consumption["job"] = []string{path}
	done := make(chan error, 1)
	go func() {
		b := make([]byte, 1024)
		for range total / len(b) {
			if _, err := f.Write(b); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 1000 {
		g.Lock()
		s, err := g.StatusLocked()
		g.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if s.UsedBytes+s.ReservedBytes != total || s.AvailableBytes != 0 {
			t.Fatalf("incoherent output snapshot: %+v", s)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
