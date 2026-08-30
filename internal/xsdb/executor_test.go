// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package xsdb

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateHWServerURL(t *testing.T) {
	for _, valid := range []string{"tcp:127.0.0.1:3121", "tcp:hw-server.local:3121", "tcp:factory_station:3121", "tcp:[::1]:3121"} {
		if err := ValidateHWServerURL(valid); err != nil {
			t.Errorf("ValidateHWServerURL(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "127.0.0.1", "tcp::3121", "tcp:host:0", "tcp:host:99999", "tcp:host&whoami:3121", "tcp:-host:3121"} {
		if err := ValidateHWServerURL(invalid); err == nil {
			t.Errorf("ValidateHWServerURL(%q) succeeded", invalid)
		}
	}
}

func TestLineWriterCombinesChunks(t *testing.T) {
	var lines []string
	writer := newLineWriter(func(line string) { lines = append(lines, line) })
	_, _ = writer.Write([]byte("first pa"))
	_, _ = writer.Write([]byte("rt\r\nsecond\npartial"))
	writer.Flush()
	got := strings.Join(lines, "|")
	if got != "first part|second|partial" {
		t.Fatalf("lines = %q", got)
	}
}

func TestRunPassesStationArgumentsAndCapturesOutput(t *testing.T) {
	directory := t.TempDir()
	entrypoint := filepath.Join(directory, "load-jtag-image.tcl")
	if err := os.WriteFile(entrypoint, []byte("puts ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	invocation := filepath.Join(directory, "invocation")
	executable := filepath.Join(directory, "xsdb")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"" + invocation + "\"\nprintf 'connected\\nbooting\\n'\n"
	if runtime.GOOS == "windows" {
		executable += ".cmd"
		script = "@echo off\r\necho %* > \"" + invocation + "\"\r\necho connected\r\necho booting\r\n"
	}
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	err := (Executor{Path: executable}).Run(context.Background(), Request{
		Entrypoint:    entrypoint,
		HWServerURL:   "tcp:127.0.0.1:3121",
		TFTPServerIP:  "192.0.2.10",
		BoardIP:       "192.0.2.20",
		WorkingFolder: directory,
	}, func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(invocation)
	if err != nil {
		t.Fatal(err)
	}
	want := entrypoint + " tcp:127.0.0.1:3121 192.0.2.10 192.0.2.20"
	if strings.TrimSpace(string(arguments)) != want {
		t.Fatalf("arguments = %q, want %q", strings.TrimSpace(string(arguments)), want)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "connected\nbooting") {
		t.Fatalf("captured lines = %v", lines)
	}
}

func TestResolvePrefersPathBeforeDefaultInstallRoot(t *testing.T) {
	clearXSDBEnvironment(t)
	pathDirectory := t.TempDir()
	pathExecutable := writeTestXSDB(t, filepath.Join(pathDirectory, executableName("xsdb")))
	installRoot := t.TempDir()
	writeTestXSDB(t, filepath.Join(installRoot, "Vitis", "2099.1", "bin", executableName("xsdb")))
	t.Setenv("PATH", pathDirectory)

	resolved, err := (Executor{}).resolve([]string{installRoot})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(pathExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("resolved=%q want=%q", resolved, want)
	}
}

func TestResolveFindsNewestDefaultXilinxInstallation(t *testing.T) {
	clearXSDBEnvironment(t)
	t.Setenv("PATH", "")
	installRoot := t.TempDir()
	writeTestXSDB(t, filepath.Join(installRoot, "Vivado", "2024.2", "bin", executableName("xsdb")))
	writeTestXSDB(t, filepath.Join(installRoot, "2025.2", "Vivado", "bin", executableName("xsdb")))
	writeTestXSDB(t, filepath.Join(installRoot, "Vitis", "2025.10", "bin", executableName("xsdb")))
	want := writeTestXSDB(t, filepath.Join(installRoot, "2026.1", "Vitis", "bin", executableName("xsdb")))

	resolved, err := (Executor{}).resolve([]string{installRoot})
	if err != nil {
		t.Fatal(err)
	}
	want, err = filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("resolved=%q want=%q", resolved, want)
	}
}

func TestDefaultInstallRootMatchesPlatform(t *testing.T) {
	roots := defaultInstallRoots()
	if len(roots) != 1 {
		t.Fatalf("roots=%v", roots)
	}
	if runtime.GOOS == "windows" {
		if roots[0] != `C:\Xilinx` {
			t.Fatalf("roots=%v", roots)
		}
	} else if roots[0] != "/opt/Xilinx" {
		t.Fatalf("roots=%v", roots)
	}
}

func clearXSDBEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"MNC_XSDB", "XILINX_VITIS", "XILINX_VIVADO"} {
		t.Setenv(name, "")
	}
}

func writeTestXSDB(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		content = "@echo off\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
