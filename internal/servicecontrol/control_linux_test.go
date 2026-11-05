// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package servicecontrol

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoninteractiveServiceCommands(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$MPS_CONTROL_LOG\"\nif [ \"$2\" = show ]; then printf 'active\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("MPS_CONTROL_LOG", log)
	ctx := context.Background()
	if err := Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Stop(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := Query(ctx)
	if err != nil || status.State != "active" {
		t.Fatalf("query = %v, %v", status, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "--no-ask-password\nstart\nmps.service\n--no-ask-password\nstop\nmps.service\n--no-ask-password\nshow\nmps.service\n--property=ActiveState\n--value\n"
	if string(data) != want {
		t.Fatalf("unexpected service commands: %s", data)
	}
}
func TestDeniedStartGivesManualCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	err := Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sudo systemctl start mps.service") {
		t.Fatalf("error = %v", err)
	}
}
