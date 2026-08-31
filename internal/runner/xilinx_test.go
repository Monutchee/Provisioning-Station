// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

type fakeXSDB struct{}

func (fakeXSDB) Resolve() (string, error) { return "xsdb", nil }

func (fakeXSDB) Run(context.Context, xsdb.Request, xsdb.LogFunc) error { return nil }

func TestExpectedTFTPFiles(t *testing.T) {
	manifest := artifact.Manifest{
		Executor: artifact.ExecutorMetadata{TFTPRoot: "tftp"},
		Files: map[string]artifact.FileDescriptor{
			"jtag/loader.tcl": {},
			"tftp/Image":      {},
			"tftp/boot.scr":   {},
		},
	}
	files := expectedTFTPFiles(manifest)
	if len(files) != 2 || files[0] != "Image" || files[1] != "boot.scr" {
		t.Fatalf("files = %v", files)
	}
}

func TestNewXilinxDefaults(t *testing.T) {
	runner, err := NewXilinx(XilinxConfig{Executor: fakeXSDB{}})
	if err != nil {
		t.Fatal(err)
	}
	if runner.config.TFTPListen != ":69" || runner.config.JobTimeout <= 0 {
		t.Fatalf("defaults were not applied: %+v", runner.config)
	}
}

func TestXilinxRejectsOldLoaderForTargetedJob(t *testing.T) {
	root := t.TempDir()
	entrypoint := filepath.Join(root, "jtag", "load-jtag-image.tcl")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("puts old-loader\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hardwareRunner, err := NewXilinx(XilinxConfig{Executor: fakeXSDB{}})
	if err != nil {
		t.Fatal(err)
	}
	err = hardwareRunner.Validate(artifact.StoredArtifact{
		RootPath: root,
		Manifest: artifact.Manifest{
			Artifact: artifact.ArtifactMetadata{Vendor: "xilinx", Operation: "jtag-boot"},
			Executor: artifact.ExecutorMetadata{
				Type: "xilinx-xsdb", Entrypoint: "jtag/load-jtag-image.tcl", TFTPRoot: "tftp",
			},
			Files: map[string]artifact.FileDescriptor{"tftp/Image": {}},
		},
	}, jobs.Request{
		HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10", TargetID: "3",
	})
	if err == nil || !strings.Contains(err.Error(), "rebuild the Station artifact") {
		t.Fatalf("error = %v", err)
	}
}

func TestXilinxRejectsV1LoaderForStableTargetJob(t *testing.T) {
	root := t.TempDir()
	entrypoint := filepath.Join(root, "jtag", "load-jtag-image.tcl")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("# MNC_STATION_TARGET_SELECTOR_V1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hardwareRunner, err := NewXilinx(XilinxConfig{Executor: fakeXSDB{}})
	if err != nil {
		t.Fatal(err)
	}
	err = hardwareRunner.Validate(artifact.StoredArtifact{
		RootPath: root,
		Manifest: artifact.Manifest{
			Artifact: artifact.ArtifactMetadata{Vendor: "xilinx", Operation: "jtag-boot"},
			Executor: artifact.ExecutorMetadata{
				Type: "xilinx-xsdb", Entrypoint: "jtag/load-jtag-image.tcl", TFTPRoot: "tftp",
			},
			Files: map[string]artifact.FileDescriptor{"tftp/Image": {}},
		},
	}, jobs.Request{
		HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10",
		TargetID: "3", TargetCableSerial: "SERIAL-A", TargetDeviceIndex: "0",
	})
	if err == nil || !strings.Contains(err.Error(), "rebuild the Station artifact") {
		t.Fatalf("error = %v", err)
	}
}

type downloadingXSDB struct {
	address  string
	files    []string
	requests chan xsdb.Request
}

func (downloadingXSDB) Resolve() (string, error) { return "xsdb", nil }
func (executor downloadingXSDB) Run(ctx context.Context, request xsdb.Request, emit xsdb.LogFunc) error {
	if executor.requests != nil {
		executor.requests <- request
	}
	for _, filename := range executor.files {
		if emit != nil {
			emit("requesting " + filename)
		}
		if err := downloadTFTPFile(ctx, executor.address, filename); err != nil {
			return err
		}
	}
	return nil
}

func TestXilinxRunnerCompletesAfterEveryTFTPFile(t *testing.T) {
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	address := probe.LocalAddr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	for name, content := range map[string]string{
		"jtag/load-jtag-image.tcl": "# MNC_STATION_TARGET_SELECTOR_V1\n# MNC_STATION_TARGET_SELECTOR_V2\nputs ok\n",
		"tftp/Image":               "kernel",
		"tftp/boot.scr":            "script",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := artifact.Manifest{
		Artifact: artifact.ArtifactMetadata{Vendor: "xilinx", Operation: "jtag-boot"},
		Executor: artifact.ExecutorMetadata{
			Type: "xilinx-xsdb", Entrypoint: "jtag/load-jtag-image.tcl", TFTPRoot: "tftp",
		},
		Files: map[string]artifact.FileDescriptor{
			"jtag/load-jtag-image.tcl": {}, "tftp/Image": {}, "tftp/boot.scr": {},
		},
	}
	requests := make(chan xsdb.Request, 1)
	hardwareRunner, err := NewXilinx(XilinxConfig{
		Executor:   downloadingXSDB{address: address, files: []string{"Image", "boot.scr"}, requests: requests},
		TFTPListen: address, TFTPTimeout: 100 * time.Millisecond, JobTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	var logMutex sync.Mutex
	err = hardwareRunner.Run(context.Background(), artifact.StoredArtifact{
		Manifest: manifest, RootPath: root,
	}, jobs.Request{
		HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "127.0.0.1", TargetID: "7",
		TargetCableSerial: "SERIAL-A", TargetDeviceIndex: "0",
	}, func(_ string, message string) {
		logMutex.Lock()
		defer logMutex.Unlock()
		logs = append(logs, message)
	})
	if err != nil {
		t.Fatal(err)
	}
	if request := <-requests; request.TargetID != "7" ||
		request.TargetCableSerial != "SERIAL-A" || request.TargetDeviceIndex != "0" {
		t.Fatalf("XSDB target request = %+v", request)
	}
	logMutex.Lock()
	joined := strings.Join(logs, "\n")
	logMutex.Unlock()
	if !strings.Contains(joined, "sent Image") || !strings.Contains(joined, "sent boot.scr") ||
		!strings.Contains(joined, "All artifact TFTP files were transferred") {
		t.Fatalf("runner logs = %s", joined)
	}
}

func downloadTFTPFile(ctx context.Context, address, filename string) error {
	serverAddress, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return err
	}
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return err
	}
	defer connection.Close()
	request := append([]byte{0, 1}, []byte(filename)...)
	request = append(request, 0)
	request = append(request, []byte("octet")...)
	request = append(request, 0)
	if _, err := connection.WriteToUDP(request, serverAddress); err != nil {
		return err
	}
	buffer := make([]byte, 1024)
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = connection.SetReadDeadline(deadline)
		} else {
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		}
		count, transferAddress, err := connection.ReadFromUDP(buffer)
		if err != nil {
			return err
		}
		if count < 4 || binary.BigEndian.Uint16(buffer[:2]) != 3 {
			continue
		}
		block := binary.BigEndian.Uint16(buffer[2:4])
		ack := []byte{0, 4, byte(block >> 8), byte(block)}
		if _, err := connection.WriteToUDP(ack, transferAddress); err != nil {
			return err
		}
		if count-4 < 512 {
			return nil
		}
	}
}
