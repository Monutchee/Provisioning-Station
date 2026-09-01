// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/tftp"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

const (
	targetSelectorMarkerV1 = "MNC_STATION_TARGET_SELECTOR_V1"
	targetSelectorMarkerV2 = "MNC_STATION_TARGET_SELECTOR_V2"
)

type XSDBExecutor interface {
	Resolve() (string, error)
	Run(context.Context, xsdb.Request, xsdb.LogFunc) error
}

type XilinxConfig struct {
	Executor     XSDBExecutor
	TFTPListen   string
	TFTPRetries  int
	TFTPTimeout  time.Duration
	MaxBlockSize int
	JobTimeout   time.Duration
	Serial       interface {
		MatchCableSerial(context.Context, string) (serialconsole.Port, string, error)
	}
}

type Xilinx struct {
	config XilinxConfig
}

func NewXilinx(config XilinxConfig) (*Xilinx, error) {
	if config.Executor == nil {
		return nil, fmt.Errorf("Xilinx runner requires an XSDB executor")
	}
	if config.TFTPListen == "" {
		config.TFTPListen = ":69"
	}
	if config.TFTPRetries == 0 {
		config.TFTPRetries = 5
	}
	if config.TFTPTimeout == 0 {
		config.TFTPTimeout = 3 * time.Second
	}
	if config.MaxBlockSize == 0 {
		config.MaxBlockSize = 1468
	}
	if config.JobTimeout == 0 {
		config.JobTimeout = 10 * time.Minute
	}
	if config.JobTimeout <= 0 {
		return nil, fmt.Errorf("job timeout must be positive")
	}
	return &Xilinx{config: config}, nil
}

func (runner *Xilinx) Run(
	ctx context.Context,
	stored artifact.StoredArtifact,
	request jobs.Request,
	emit func(level, message string),
) error {
	if err := runner.Validate(stored, request); err != nil {
		return err
	}
	manifest := stored.Manifest

	entrypoint := filepath.Join(stored.RootPath, filepath.FromSlash(manifest.Executor.Entrypoint))
	tftpRoot := filepath.Join(stored.RootPath, filepath.FromSlash(manifest.Executor.TFTPRoot))
	expected := expectedTFTPFiles(manifest)

	runContext, cancel := context.WithTimeout(ctx, runner.config.JobTimeout)
	defer cancel()
	tftpConfig := tftp.DefaultConfig(tftpRoot)
	tftpConfig.ListenAddress = runner.config.TFTPListen
	tftpConfig.AllowedClientIP = request.BoardIP
	tftpConfig.Retries = runner.config.TFTPRetries
	tftpConfig.Timeout = runner.config.TFTPTimeout
	tftpConfig.MaxBlockSize = runner.config.MaxBlockSize
	tftpServer, err := tftp.Listen(tftpConfig)
	if err != nil {
		return err
	}
	defer tftpServer.Close()
	emit("info", fmt.Sprintf("TFTP server listening on %s with root %s", tftpServer.Addr(), manifest.Executor.TFTPRoot))
	emit("info", "Waiting for TFTP files: "+strings.Join(expected, ", "))

	serveDone := make(chan error, 1)
	go func() { serveDone <- tftpServer.Serve(runContext) }()
	allTransfersDone := make(chan struct{})
	eventsDone := make(chan struct{})
	go monitorTransfers(runContext, tftpServer.Events(), expected, allTransfersDone, eventsDone, emit)

	xsdbErr := runner.config.Executor.Run(runContext, xsdb.Request{
		Entrypoint:        entrypoint,
		HWServerURL:       request.HWServerURL,
		TFTPServerIP:      request.TFTPServerIP,
		BoardIP:           request.BoardIP,
		TargetID:          request.TargetID,
		TargetCableSerial: request.TargetCableSerial,
		TargetDeviceIndex: request.TargetDeviceIndex,
		WorkingFolder:     filepath.Dir(entrypoint),
	}, func(line string) {
		if line != "" {
			emit("info", "xsdb: "+line)
		}
	})
	if xsdbErr != nil {
		cancel()
		_ = tftpServer.Close()
		<-serveDone
		<-eventsDone
		return xsdbErr
	}
	emit("info", "XSDB loader completed; waiting for the board to finish all TFTP transfers")

	select {
	case <-allTransfersDone:
		emit("info", "All artifact TFTP files were transferred")
	case err := <-serveDone:
		cancel()
		<-eventsDone
		if runContext.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("JTAG boot timed out after %s", runner.config.JobTimeout)
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("TFTP server stopped before all files were transferred")
	case <-runContext.Done():
		cancel()
		_ = tftpServer.Close()
		<-serveDone
		<-eventsDone
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("JTAG boot timed out after %s", runner.config.JobTimeout)
	}
	cancel()
	_ = tftpServer.Close()
	<-serveDone
	<-eventsDone
	return nil
}

func (runner *Xilinx) Validate(stored artifact.StoredArtifact, request jobs.Request) error {
	manifest := stored.Manifest
	if manifest.Artifact.Vendor != "xilinx" || manifest.Artifact.Operation != "jtag-boot" {
		return fmt.Errorf("artifact vendor/operation %s/%s is not supported by the Xilinx JTAG runner", manifest.Artifact.Vendor, manifest.Artifact.Operation)
	}
	if manifest.Executor.Type != "xilinx-xsdb" {
		return fmt.Errorf("unsupported executor type %q", manifest.Executor.Type)
	}
	if manifest.Executor.TFTPRoot == "" {
		return fmt.Errorf("Xilinx JTAG artifact has no TFTP root")
	}
	if err := xsdb.ValidateHWServerURL(request.HWServerURL); err != nil {
		return err
	}
	if err := xsdb.ValidateIPv4("TFTP server IP", request.TFTPServerIP, false); err != nil {
		return err
	}
	if err := xsdb.ValidateIPv4("board IP", request.BoardIP, true); err != nil {
		return err
	}
	if err := xsdb.ValidateTargetID(request.TargetID); err != nil {
		return err
	}
	if err := xsdb.ValidateTargetCableSerial(request.TargetCableSerial); err != nil {
		return err
	}
	if err := xsdb.ValidateTargetDeviceIndex(request.TargetDeviceIndex); err != nil {
		return err
	}
	if request.TargetDeviceIndex != "" && request.TargetCableSerial == "" {
		return fmt.Errorf("targetDeviceIndex requires targetCableSerial")
	}
	if request.SerialConsole != nil {
		if request.SerialConsole.PortID == "" {
			return fmt.Errorf("serialConsole.portId must not be empty")
		}
		if err := serialconsole.ValidateBaudRate(request.SerialConsole.BaudRate); request.SerialConsole.BaudRate != 0 && err != nil {
			return err
		}
		if request.TargetCableSerial == "" {
			return fmt.Errorf("serialConsole requires targetCableSerial")
		}
		if runner.config.Serial == nil {
			return fmt.Errorf("serial console service is unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		port, _, err := runner.config.Serial.MatchCableSerial(ctx, request.TargetCableSerial)
		if err != nil {
			return fmt.Errorf("match serial console to JTAG cable %q: %w", request.TargetCableSerial, err)
		}
		if port.ID != request.SerialConsole.PortID {
			return fmt.Errorf("serialConsole.portId does not belong to JTAG cable %q", request.TargetCableSerial)
		}
	}
	if request.TargetID != "" || request.TargetCableSerial != "" {
		entrypoint := filepath.Join(stored.RootPath, filepath.FromSlash(manifest.Executor.Entrypoint))
		loader, err := os.ReadFile(entrypoint)
		if err != nil {
			return fmt.Errorf("read Xilinx JTAG loader: %w", err)
		}
		marker := targetSelectorMarkerV1
		if request.TargetCableSerial != "" {
			marker = targetSelectorMarkerV2
		}
		if !bytes.Contains(loader, []byte(marker)) {
			return fmt.Errorf("artifact loader does not support selecting a JTAG device; rebuild the Station artifact")
		}
	}
	if _, err := runner.config.Executor.Resolve(); err != nil {
		return err
	}
	expected := expectedTFTPFiles(manifest)
	if len(expected) == 0 {
		return fmt.Errorf("artifact TFTP root has no payload files")
	}
	return nil
}

func expectedTFTPFiles(manifest artifact.Manifest) []string {
	prefix := manifest.Executor.TFTPRoot + "/"
	files := make([]string, 0)
	for path := range manifest.Files {
		if strings.HasPrefix(path, prefix) {
			files = append(files, strings.TrimPrefix(path, prefix))
		}
	}
	sort.Strings(files)
	return files
}

func monitorTransfers(
	ctx context.Context,
	events <-chan tftp.TransferEvent,
	expected []string,
	allDone chan<- struct{},
	done chan<- struct{},
	emit func(level, message string),
) {
	defer close(done)
	wanted := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		wanted[name] = struct{}{}
	}
	completed := make(map[string]struct{}, len(expected))
	var signalOnce sync.Once
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			switch event.Status {
			case "started":
				emit("info", fmt.Sprintf("tftp: request %s from %s", event.Filename, event.Remote))
			case "completed":
				emit("info", fmt.Sprintf("tftp: sent %s (%d bytes)", event.Filename, event.Bytes))
				if _, ok := wanted[event.Filename]; ok {
					completed[event.Filename] = struct{}{}
				}
				if len(completed) == len(wanted) {
					signalOnce.Do(func() { close(allDone) })
				}
			case "failed":
				emit("warning", fmt.Sprintf("tftp: %s failed: %s", event.Filename, event.Error))
			}
		}
	}
}
