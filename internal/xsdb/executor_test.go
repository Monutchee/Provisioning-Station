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

func TestValidateTargetID(t *testing.T) {
	for _, valid := range []string{"", "1", "42"} {
		if err := ValidateTargetID(valid); err != nil {
			t.Errorf("ValidateTargetID(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"0", "-1", "01", "1.0", "1; puts bad"} {
		if err := ValidateTargetID(invalid); err == nil {
			t.Errorf("ValidateTargetID(%q) succeeded", invalid)
		}
	}
}

func TestValidateStableTargetIdentity(t *testing.T) {
	for _, valid := range []string{"", "XFL1YADUCAY1A", "serial with spaces"} {
		if err := ValidateTargetCableSerial(valid); err != nil {
			t.Errorf("ValidateTargetCableSerial(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"bad\nserial", strings.Repeat("x", 257)} {
		if err := ValidateTargetCableSerial(invalid); err == nil {
			t.Errorf("ValidateTargetCableSerial(%q) succeeded", invalid)
		}
	}
	for _, valid := range []string{"", "0", "1", "42"} {
		if err := ValidateTargetDeviceIndex(valid); err != nil {
			t.Errorf("ValidateTargetDeviceIndex(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"-1", "01", "1.0"} {
		if err := ValidateTargetDeviceIndex(invalid); err == nil {
			t.Errorf("ValidateTargetDeviceIndex(%q) succeeded", invalid)
		}
	}
}

func TestParseTargets(t *testing.T) {
	output := "XSDB banner\n" + targetMarker +
		"\t37\t505355\t30\t78637a75346576\t446967696c656e7420555342\t\n"
	targets, err := parseTargets([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "7" || targets[0].Name != "PSU" ||
		targets[0].DeviceIndex != "0" || targets[0].DeviceName != "xczu4ev" ||
		targets[0].CableName != "Digilent USB" || targets[0].CableSerial != "" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestDiscoverRunsXSDBAndParsesTargets(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "xsdb")
	record := targetMarker + "\t39\t505355\t30\t78637a75346576\t446967696c656e7420555342\t53455249414c2d42"
	script := "#!/bin/sh\nprintf '%s\\n' '" + record + "'\n"
	if runtime.GOOS == "windows" {
		executable += ".cmd"
		script = "@echo off\r\necho " + record + "\r\n"
	}
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	targets, err := (Executor{Path: executable}).Discover(context.Background(), "tcp:127.0.0.1:3121")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "9" || targets[0].CableSerial != "SERIAL-B" {
		t.Fatalf("targets = %+v", targets)
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
		Entrypoint:        entrypoint,
		HWServerURL:       "tcp:127.0.0.1:3121",
		TFTPServerIP:      "192.0.2.10",
		BoardIP:           "192.0.2.20",
		TargetID:          "17",
		TargetCableSerial: "SERIAL-A",
		TargetDeviceIndex: "1",
		WorkingFolder:     directory,
	}, func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(invocation)
	if err != nil {
		t.Fatal(err)
	}
	want := entrypoint + " tcp:127.0.0.1:3121 192.0.2.10 192.0.2.20 17 SERIAL-A 1"
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
	pathExecutable := writeTestTool(t, filepath.Join(pathDirectory, executableName("xsdb")))
	installRoot := t.TempDir()
	writeTestTool(t, filepath.Join(installRoot, "Vitis", "2099.1", "bin", executableName("xsdb")))
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
	writeTestTool(t, filepath.Join(installRoot, "Vivado", "2024.2", "bin", executableName("xsdb")))
	writeTestTool(t, filepath.Join(installRoot, "2025.2", "Vivado", "bin", executableName("xsdb")))
	writeTestTool(t, filepath.Join(installRoot, "Vitis", "2025.10", "bin", executableName("xsdb")))
	want := writeTestTool(t, filepath.Join(installRoot, "2026.1", "Vitis", "bin", executableName("xsdb")))

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

func TestResolveFindsHWSRVRInstallation(t *testing.T) {
	clearXSDBEnvironment(t)
	t.Setenv("PATH", "")

	for _, relative := range [][]string{
		{"2025.2", "HWSRVR", "bin", executableName("xsdb")},
		{"HWSRVR", "2025.2", "bin", executableName("xsdb")},
	} {
		t.Run(filepath.Join(relative...), func(t *testing.T) {
			installRoot := t.TempDir()
			want := writeTestTool(t, filepath.Join(append([]string{installRoot}, relative...)...))

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
		})
	}
}

func TestResolveHWServerFindsHWSRVRInstallation(t *testing.T) {
	clearXSDBEnvironment(t)
	t.Setenv("PATH", "")
	installRoot := t.TempDir()
	want := writeTestTool(t, filepath.Join(
		installRoot, "2025.2", "HWSRVR", "bin", executableName("hw_server"),
	))

	resolved, err := resolveHWServer("", []string{installRoot})
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

func TestResolveRejectsNonExecutableTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows executable permissions do not use Unix mode bits")
	}
	path := filepath.Join(t.TempDir(), "hw_server")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHWServer(path, nil); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("error=%v", err)
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
	for _, name := range []string{"MNC_XSDB", "MNC_HW_SERVER", "XILINX_VITIS", "XILINX_VIVADO"} {
		t.Setenv(name, "")
	}
}

func writeTestTool(t *testing.T, path string) string {
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
