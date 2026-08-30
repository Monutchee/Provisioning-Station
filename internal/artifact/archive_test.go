// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type archiveEntry struct {
	name     string
	data     []byte
	mode     int64
	typeflag byte
	linkname string
}

func TestValidateAndExtract(t *testing.T) {
	archive := writeTestArchive(t, nil)
	destination := filepath.Join(t.TempDir(), "payload")
	extraction, err := ValidateAndExtract(context.Background(), archive, destination, DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateAndExtract() error = %v", err)
	}
	if extraction.Manifest.Artifact.Name != "msap1-jtag-image" {
		t.Fatalf("unexpected artifact name %q", extraction.Manifest.Artifact.Name)
	}
	if extraction.PayloadFiles != 3 {
		t.Fatalf("PayloadFiles = %d, want 3", extraction.PayloadFiles)
	}
	loader, err := os.ReadFile(filepath.Join(destination, "jtag", "load-jtag-image.tcl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(loader) != "puts ok\n" {
		t.Fatalf("loader content = %q", loader)
	}
	info, err := os.Stat(filepath.Join(destination, "jtag", "load-jtag-image.tcl"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose POSIX execute bits through os.FileMode. The tar
	// header and manifest mode are still validated by ValidateAndExtract.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("loader mode = %o, want executable", info.Mode().Perm())
	}
}

func TestArchiveSafetyFailures(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func([]archiveEntry) []archiveEntry
		message string
	}{
		{
			name: "traversal",
			mutate: func(entries []archiveEntry) []archiveEntry {
				entries[1].name = "../escape"
				return entries
			},
			message: "unsafe path component",
		},
		{
			name: "symlink",
			mutate: func(entries []archiveEntry) []archiveEntry {
				entries[1].typeflag = tar.TypeSymlink
				entries[1].linkname = "elsewhere"
				entries[1].data = nil
				return entries
			},
			message: "not a regular file",
		},
		{
			name: "duplicate",
			mutate: func(entries []archiveEntry) []archiveEntry {
				return append(entries, entries[len(entries)-1])
			},
			message: "duplicate path",
		},
		{
			name: "unsorted",
			mutate: func(entries []archiveEntry) []archiveEntry {
				entries[1], entries[2] = entries[2], entries[1]
				return entries
			},
			message: "not sorted",
		},
		{
			name: "tampered payload",
			mutate: func(entries []archiveEntry) []archiveEntry {
				entries[len(entries)-1].data = []byte("xxxxxx")
				return entries
			},
			message: "SHA-256 differs",
		},
		{
			name: "non-normalized mode",
			mutate: func(entries []archiveEntry) []archiveEntry {
				entries[1].mode = 0o4755
				return entries
			},
			message: "non-normalized mode",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := writeTestArchive(t, test.mutate)
			_, err := ValidateAndExtract(
				context.Background(), archive, filepath.Join(t.TempDir(), "payload"), DefaultLimits(),
			)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestArchiveRejectsDataAfterTarEnd(t *testing.T) {
	archive := writeTestArchive(t, nil)
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	additional := gzip.NewWriter(file)
	if _, err := additional.Write([]byte("unexpected trailing stream")); err != nil {
		t.Fatal(err)
	}
	if err := additional.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = ValidateAndExtract(
		context.Background(), archive, filepath.Join(t.TempDir(), "payload"), DefaultLimits(),
	)
	if err == nil || !strings.Contains(err.Error(), "after the tar end") {
		t.Fatalf("trailing data error = %v", err)
	}
}

func TestManifestRejectsUnknownAndDuplicateFields(t *testing.T) {
	manifest, _ := testManifest(t)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(data, []byte(`"formatVersion":2`), []byte(`"formatVersion":2,"future":true`), 1)
	if _, err := DecodeManifest(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicate := bytes.Replace(data, []byte(`"schema":"mnc-station-artifact"`), []byte(`"schema":"mnc-station-artifact","schema":"mnc-station-artifact"`), 1)
	if _, err := DecodeManifest(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestStoreIsContentAddressedAndEnforcesUploadLimit(t *testing.T) {
	archive := writeTestArchive(t, nil)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxCompressedBytes = int64(len(data))
	store, err := OpenStore(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Import(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Import(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate import IDs differ: %s != %s", first.ID, second.ID)
	}
	if _, err := os.Stat(first.ArchivePath); err != nil {
		t.Fatalf("stored archive: %v", err)
	}
	if err := store.Verify(context.Background(), first); err != nil {
		t.Fatalf("verify stored artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first.RootPath, "tftp", "Image"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(context.Background(), first); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("tampered store verification error = %v", err)
	}

	limits.MaxCompressedBytes = int64(len(data) - 1)
	limitedStore, err := OpenStore(filepath.Join(t.TempDir(), "limited"), limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limitedStore.Import(context.Background(), bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "compressed size limit") {
		t.Fatalf("limit error = %v", err)
	}
}

func writeTestArchive(t *testing.T, mutate func([]archiveEntry) []archiveEntry) string {
	t.Helper()
	manifest, payload := testManifest(t)
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	entries := []archiveEntry{{name: ManifestName, data: manifestData, mode: 0o644, typeflag: tar.TypeReg}}
	for _, name := range SortedPayloadPaths(manifest) {
		mode := int64(0o644)
		if manifest.Files[name].Mode == "0755" {
			mode = 0o755
		}
		entries = append(entries, archiveEntry{name: name, data: payload[name], mode: mode, typeflag: tar.TypeReg})
	}
	if mutate != nil {
		entries = mutate(entries)
	}

	path := filepath.Join(t.TempDir(), "artifact.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.data)),
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
			Uid:      0,
			Gid:      0,
			Format:   tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tarWriter, bytes.NewReader(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func testManifest(t *testing.T) (Manifest, map[string][]byte) {
	t.Helper()
	payload := map[string][]byte{
		"jtag/load-jtag-image.tcl": []byte("puts ok\n"),
		"tftp/Image":               []byte("kernel"),
		"tftp/boot.scr":            []byte("script"),
	}
	files := make(map[string]FileDescriptor, len(payload))
	for name, data := range payload {
		digest := sha256.Sum256(data)
		mode := "0644"
		if name == "jtag/load-jtag-image.tcl" {
			mode = "0755"
		}
		files[name] = FileDescriptor{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: mode}
	}
	return Manifest{
		Schema:        SchemaName,
		FormatVersion: FormatVersion,
		Artifact: ArtifactMetadata{
			Name:       "msap1-jtag-image",
			Vendor:     "xilinx",
			Operation:  "jtag-boot",
			Product:    "msap1",
			Machine:    "msap1",
			Version:    "0.0.1",
			BuildID:    "20260830135554",
			CreatedUTC: "2026-08-30T13:55:54Z",
		},
		Executor: ExecutorMetadata{
			Type:       "xilinx-xsdb",
			Entrypoint: "jtag/load-jtag-image.tcl",
			TFTPRoot:   "tftp",
		},
		Files: files,
	}, payload
}
