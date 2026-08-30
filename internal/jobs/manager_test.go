// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

type controlledRunner struct {
	started chan string
	release chan struct{}
}

func (runner *controlledRunner) Validate(artifact.StoredArtifact, Request) error { return nil }

func (runner *controlledRunner) Run(ctx context.Context, stored artifact.StoredArtifact, request Request, emit func(string, string)) error {
	emit("info", "runner executing "+stored.Manifest.Artifact.Name)
	runner.started <- request.ArtifactID
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runner.release:
		return nil
	}
}

func TestManagerSerializesAndPersistsJobs(t *testing.T) {
	storeRoot := t.TempDir()
	store, err := artifact.OpenStore(storeRoot, artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	runner := &controlledRunner{started: make(chan string, 2), release: make(chan struct{}, 2)}
	jobRoot := filepath.Join(t.TempDir(), "jobs")
	manager, err := OpenManager(jobRoot, store, runner)
	if err != nil {
		t.Fatal(err)
	}

	request := Request{ArtifactID: stored.ID, HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10"}
	first, err := manager.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
	}
	select {
	case <-runner.started:
		t.Fatal("second job started before the first was released")
	case <-time.After(30 * time.Millisecond):
	}
	runner.release <- struct{}{}
	waitForState(t, manager, first.ID, StateSucceeded)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("second job did not start")
	}
	runner.release <- struct{}{}
	waitForState(t, manager, second.ID, StateSucceeded)
	events, err := manager.Events(first.ID, 0)
	if err != nil || len(events) < 4 {
		t.Fatalf("events = %v, error = %v", events, err)
	}
	manager.Close()

	reopenedRunner := &controlledRunner{started: make(chan string, 1), release: make(chan struct{}, 1)}
	reopened, err := OpenManager(jobRoot, store, reopenedRunner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateSucceeded || loaded.EventCount != len(events) {
		t.Fatalf("reloaded job = %+v, events=%d", loaded, len(events))
	}
}

func TestCancelRunningJob(t *testing.T) {
	store, err := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	runner := &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := OpenManager(filepath.Join(t.TempDir(), "jobs"), store, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	job, err := manager.Create(Request{ArtifactID: stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	if _, err := manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, job.ID, StateCanceled)
}

func waitForState(t *testing.T, manager *Manager, id string, want State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := manager.Get(id)
	t.Fatalf("job state = %s, want %s", job.State, want)
}

func testArtifact(t *testing.T) []byte {
	t.Helper()
	payload := map[string][]byte{
		"jtag/load-jtag-image.tcl": []byte("puts ok\n"),
		"tftp/Image":               []byte("kernel"),
	}
	files := make(map[string]artifact.FileDescriptor)
	for name, data := range payload {
		digest := sha256.Sum256(data)
		mode := "0644"
		if name == "jtag/load-jtag-image.tcl" {
			mode = "0755"
		}
		files[name] = artifact.FileDescriptor{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: mode}
	}
	manifest := artifact.Manifest{
		Schema: artifact.SchemaName, FormatVersion: artifact.FormatVersion,
		Artifact: artifact.ArtifactMetadata{
			Name: "msap1-jtag-image", Vendor: "xilinx", Operation: "jtag-boot",
			Product: "msap1", Machine: "msap1", Version: "1", BuildID: "test",
			CreatedUTC: "2026-08-30T00:00:00Z",
		},
		Executor: artifact.ExecutorMetadata{Type: "xilinx-xsdb", Entrypoint: "jtag/load-jtag-image.tcl", TFTPRoot: "tftp"},
		Files:    files,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := append([]string{artifact.ManifestName}, artifact.SortedPayloadPaths(manifest)...)
	for _, name := range entries {
		data := payload[name]
		mode := int64(0o644)
		if name == artifact.ManifestName {
			data = manifestData
		} else if manifest.Files[name].Mode == "0755" {
			mode = 0o755
		}
		header := &tar.Header{Name: name, Size: int64(len(data)), Mode: mode, Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Format: tar.FormatUSTAR}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
