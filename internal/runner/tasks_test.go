// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/tasks"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

type hookOrderXSDB struct {
	prepared *bool
	called   *bool
}

func (hookOrderXSDB) Resolve() (string, error) { return "synthetic-xsdb", nil }
func (executor hookOrderXSDB) Run(context.Context, xsdb.Request, xsdb.LogFunc) error {
	*executor.called = true
	if !*executor.prepared {
		return errors.New("XSDB invoked before preparation")
	}
	return errors.New("synthetic loader completion")
}
func TestBeforeBootRunsAfterTFTPStartupAndBlocksXSDBOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "order", true: "failure"}[fail], func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "tftp"), 0755); err != nil {
				t.Fatal(err)
			}
			prepared, called := false, false
			hardware, err := NewXilinx(XilinxConfig{Executor: hookOrderXSDB{prepared: &prepared, called: &called}, TFTPListen: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			ctx := tasks.WithBeforeBoot(context.Background(), func(context.Context) error {
				if !ready {
					t.Error("hook launched before TFTP startup")
				}
				prepared = true
				if fail {
					return errors.New("synthetic preparation failed")
				}
				return nil
			})
			stored := artifact.StoredArtifact{RootPath: root, Manifest: artifact.Manifest{Artifact: artifact.ArtifactMetadata{Vendor: "xilinx", Operation: "jtag-boot"}, Executor: artifact.ExecutorMetadata{Type: "xilinx-xsdb", Entrypoint: "loader.tcl", TFTPRoot: "tftp"}, Files: map[string]artifact.FileDescriptor{"tftp/Image": {}}}}
			err = hardware.Run(ctx, stored, jobs.Request{HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "127.0.0.1"}, func(_ string, message string) {
				if strings.HasPrefix(message, "TFTP server listening") {
					ready = true
				}
			})
			if err == nil || !prepared || called == fail {
				t.Fatalf("err=%v prepared=%v called=%v", err, prepared, called)
			}
		})
	}
}
