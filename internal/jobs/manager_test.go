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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
)

type controlledRunner struct {
	started chan string
	release chan struct{}
}

type jobSerialDiscoverer struct{ port serialconsole.Port }

func (discoverer jobSerialDiscoverer) Discover(context.Context) (serialconsole.Discovery, error) {
	return serialconsole.Discovery{Ports: []serialconsole.Port{discoverer.port}}, nil
}

type jobSerialPort struct {
	reads  chan []byte
	closed chan struct{}
	once   sync.Once
}

func newJobSerialPort() *jobSerialPort {
	return &jobSerialPort{reads: make(chan []byte, 8), closed: make(chan struct{})}
}

func (port *jobSerialPort) Read(destination []byte) (int, error) {
	select {
	case data := <-port.reads:
		return copy(destination, data), nil
	case <-port.closed:
		return 0, io.EOF
	}
}

func (port *jobSerialPort) Write(data []byte) (int, error) { return len(data), nil }
func (port *jobSerialPort) Close() error {
	port.once.Do(func() { close(port.closed) })
	return nil
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

func TestJobCapturesSerialBeforeRunnerAndTruncatesRetention(t *testing.T) {
	store, err := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	serialPort := serialconsole.Port{
		ID: "serial-port-a", Name: "/dev/ttyUSB1", VendorID: serialconsole.FTDIVendorID,
		ProductID: serialconsole.FT2232HProductID, USBSerial: "BOARD-A", Channel: serialconsole.FT2232HUARTChannel,
	}
	handle := newJobSerialPort()
	console, err := serialconsole.New(serialconsole.Config{
		Discoverer: jobSerialDiscoverer{port: serialPort},
		Open:       func(string, int) (serialconsole.PortHandle, error) { return handle, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	runner := &controlledRunner{started: make(chan string, 1), release: make(chan struct{}, 1)}
	jobRoot := filepath.Join(t.TempDir(), "jobs")
	manager, err := OpenManager(jobRoot, store, runner, WithSerialConsole(console, 4))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	job, err := manager.Create(Request{
		ArtifactID:    stored.ID,
		SerialConsole: &SerialConsoleRequest{PortID: serialPort.ID, BaudRate: 115200},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start after serial capture attached")
	}
	handle.reads <- []byte("abcdef")
	transcriptPath := filepath.Join(jobRoot, job.ID, serialTranscriptName)
	deadline := time.Now().Add(time.Second)
	for {
		info, statErr := os.Stat(transcriptPath)
		if statErr == nil && info.Size() == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serial transcript was not written: info=%v err=%v", info, statErr)
		}
		time.Sleep(time.Millisecond)
	}
	runner.release <- struct{}{}
	waitForState(t, manager, job.ID, StateSucceeded)
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcd" {
		t.Fatalf("transcript = %q", data)
	}
	finished, err := manager.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.SerialCapture == nil || !finished.SerialCapture.Truncated ||
		finished.SerialCapture.ReceivedBytes != 6 || finished.SerialCapture.RetainedBytes != 4 ||
		finished.SerialCapture.State != "truncated" {
		t.Fatalf("serial capture = %+v", finished.SerialCapture)
	}
}

func TestSerialOpenFailureStopsJobBeforeRunner(t *testing.T) {
	store, err := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	serialPort := serialconsole.Port{ID: "serial-port-a", Name: "/dev/ttyUSB1"}
	console, err := serialconsole.New(serialconsole.Config{
		Discoverer: jobSerialDiscoverer{port: serialPort},
		Open:       func(string, int) (serialconsole.PortHandle, error) { return nil, errors.New("access denied") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	runner := &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := OpenManager(filepath.Join(t.TempDir(), "jobs"), store, runner, WithSerialConsole(console, 1024))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	job, err := manager.Create(Request{
		ArtifactID: stored.ID, SerialConsole: &SerialConsoleRequest{PortID: serialPort.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, job.ID, StateFailed)
	select {
	case <-runner.started:
		t.Fatal("runner started after serial console open failed")
	default:
	}
	failed, err := manager.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failed.Error, "access denied") {
		t.Fatalf("job error = %q", failed.Error)
	}
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
