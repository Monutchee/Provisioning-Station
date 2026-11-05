// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package artifact

import (
	"context"
	"errors"
	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
	"os"
	"path/filepath"
	"testing"
)

func TestImportBudgetIncludesExtractionAndCleansFailedStaging(t *testing.T) {
	archive := writeTestArchive(t, nil)
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := OpenStore(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	g := storagebudget.New(root, info.Size()+10)
	store.SetBudget(g)
	_, err = store.ImportFile(context.Background(), archive)
	if !errors.Is(err, storagebudget.ErrFull) {
		t.Fatalf("import=%v", err)
	}
	size, err := storagebudget.Size(root)
	if err != nil || size != 0 {
		t.Fatalf("staging retained %d %v", size, err)
	}
}
func TestStartupStagingCleanupLeavesUnknownFiles(t *testing.T) {
	store, err := OpenStore(t.TempDir(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(store.temporary, ".upload-1234.tar.gz"), filepath.Join(store.temporary, "keep.txt")} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(store.artifacts, ".import-1234"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.temporary, "keep.txt")); err != nil {
		t.Fatal("unknown file removed")
	}
	if _, err := os.Stat(filepath.Join(store.artifacts, ".import-1234")); !os.IsNotExist(err) {
		t.Fatal("staging directory retained")
	}
}

func TestStartupRecoversAtomicallyRenamedArtifactDeletion(t *testing.T) {
	store, err := OpenStore(t.TempDir(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.ImportFile(context.Background(), writeTestArchive(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Model a crash after publishing deletion intent but before unlinking files.
	staging := filepath.Join(store.artifacts, ".delete-"+a.ID+"-1234")
	if err := os.Rename(store.Directory(a.ID), staging); err != nil {
		t.Fatal(err)
	}
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("deleted artifact remains listed: %v %v", list, err)
	}
	if err := store.CleanStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("deletion staging retained")
	}
	if _, err := store.ImportFile(context.Background(), writeTestArchive(t, nil)); err != nil {
		t.Fatalf("reimport failed: %v", err)
	}
}
