// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Test-only container fixture. Imports and verifies payloads without hardware.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: fixture seed|verify STATE")
	}
	store, err := artifact.OpenStore(filepath.Join(os.Args[2], "store"), artifact.DefaultLimits())
	if err != nil {
		return err
	}
	if os.Args[1] == "seed" {
		payload := map[string][]byte{"jtag/load.tcl": []byte("puts fixture\n"), "tftp/Image": []byte("retained kernel\n")}
		manifest := artifact.Manifest{Schema: artifact.SchemaName, FormatVersion: 2,
			Artifact: artifact.ArtifactMetadata{Name: "fixture", Vendor: "xilinx", Operation: "jtag-boot", Product: "fixture", Machine: "fixture", Version: "1.0.0", BuildID: "fixture", CreatedUTC: "2026-11-05T00:00:00Z"},
			Executor: artifact.ExecutorMetadata{Type: "xilinx-xsdb", Entrypoint: "jtag/load.tcl", TFTPRoot: "tftp"}, Files: map[string]artifact.FileDescriptor{}}
		for name, data := range payload {
			digest := sha256.Sum256(data)
			mode := "0644"
			if name == "jtag/load.tcl" {
				mode = "0755"
			}
			manifest.Files[name] = artifact.FileDescriptor{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: mode}
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		var archive bytes.Buffer
		gz := gzip.NewWriter(&archive)
		tw := tar.NewWriter(gz)
		payload[artifact.ManifestName] = data
		for _, name := range []string{artifact.ManifestName, "jtag/load.tcl", "tftp/Image"} {
			data := payload[name]
			mode := int64(0644)
			if name == "jtag/load.tcl" {
				mode = 0755
			}
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		_, err = store.Import(context.Background(), bytes.NewReader(archive.Bytes()))
		return err
	}
	if os.Args[1] != "verify" {
		return fmt.Errorf("unknown fixture operation")
	}
	entries, err := os.ReadDir(filepath.Join(os.Args[2], "store/artifacts"))
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("expected one retained artifact")
	}
	record, err := store.Load(entries[0].Name())
	if err != nil {
		return err
	}
	return store.Verify(context.Background(), record)
}
