// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunXilinxHWServerCheck(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "hw_server")
	if err := os.WriteFile(executable, []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNC_HW_SERVER", executable)
	t.Setenv("MNC_HW_SERVER_LISTEN", "tcp:127.0.0.1:3121")
	if err := runXilinxHWServer([]string{"--check"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunXilinxHWServerUsesLoopbackAndPassesArguments(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "hw_server")
	if err := os.WriteFile(executable, []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNC_HW_SERVER", executable)
	t.Setenv("MNC_HW_SERVER_LISTEN", "")
	var resolved string
	var arguments []string
	err := runXilinxHWServerWithExecutor([]string{"--", "-q"}, func(path string, values []string) error {
		resolved = path
		arguments = append([]string(nil), values...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != executable {
		t.Fatalf("resolved=%q want=%q", resolved, executable)
	}
	want := []string{"-stcp:127.0.0.1:3121", "-q"}
	if strings.Join(arguments, " ") != strings.Join(want, " ") {
		t.Fatalf("arguments=%q want=%q", arguments, want)
	}
}

func TestRunXilinxHWServerRejectsInvalidListenURL(t *testing.T) {
	if err := runXilinxHWServer([]string{"--check", "--listen=not-a-url"}); err == nil ||
		!strings.Contains(err.Error(), "invalid hw_server listen URL") {
		t.Fatalf("error=%v", err)
	}
}

func TestDefaultServeConfigListensOnAllIPv4Interfaces(t *testing.T) {
	t.Setenv("MNC_STATION_HTTP_LISTEN", "")
	config, err := defaultServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTPListen != "0.0.0.0:8042" {
		t.Fatalf("HTTPListen=%q", config.HTTPListen)
	}
}

func TestDefaultServeConfigReadsSerialLimits(t *testing.T) {
	t.Setenv("MNC_STATION_SERIAL_BAUD", "921600")
	t.Setenv("MNC_STATION_MAX_CONSOLE_LOG_BYTES", "1048576")
	config, err := defaultServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.SerialBaud != 921600 || config.MaxConsoleLogBytes != 1048576 {
		t.Fatalf("serial config = baud %d, log %d", config.SerialBaud, config.MaxConsoleLogBytes)
	}
}

func TestDefaultServeConfigRejectsInvalidSerialEnvironment(t *testing.T) {
	t.Setenv("MNC_STATION_SERIAL_BAUD", "fast")
	if _, err := defaultServeConfig(); err == nil || !strings.Contains(err.Error(), "MNC_STATION_SERIAL_BAUD") {
		t.Fatalf("error = %v", err)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8042", "[::1]:8042", "localhost:8042"} {
		if !isLoopbackListen(address) {
			t.Errorf("isLoopbackListen(%q) = false", address)
		}
	}
	for _, address := range []string{":8042", "0.0.0.0:8042", "192.0.2.4:8042", "broken"} {
		if isLoopbackListen(address) {
			t.Errorf("isLoopbackListen(%q) = true", address)
		}
	}
}

func TestLoadAPIToken(t *testing.T) {
	token, err := loadAPIToken("0123456789abcdef", "")
	if err != nil || token != "0123456789abcdef" {
		t.Fatalf("token=%q error=%v", token, err)
	}
	if _, err := loadAPIToken("short", ""); err == nil {
		t.Fatal("short token was accepted")
	}
}

func TestResolveAPITokenCreatesManagedTokenForRemoteListener(t *testing.T) {
	dataDirectory := t.TempDir()
	config := serveConfig{HTTPListen: "0.0.0.0:8042", DataDir: dataDirectory}
	token, tokenFile, err := resolveAPIToken(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 48 {
		t.Fatalf("generated token length=%d", len(token))
	}
	if tokenFile != filepath.Join(dataDirectory, "api-token") {
		t.Fatalf("tokenFile=%q", tokenFile)
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("token mode=%#o", info.Mode().Perm())
	}

	again, againFile, err := resolveAPIToken(config)
	if err != nil {
		t.Fatal(err)
	}
	if again != token || againFile != tokenFile {
		t.Fatal("managed token was not reused")
	}
}

func TestResolveAPITokenLeavesLoopbackUnauthenticated(t *testing.T) {
	config := serveConfig{HTTPListen: "127.0.0.1:8042", DataDir: t.TempDir()}
	token, tokenFile, err := resolveAPIToken(config)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || tokenFile != "" {
		t.Fatalf("token=%q tokenFile=%q", token, tokenFile)
	}
}

func TestExistingAPITokenReadsManagedToken(t *testing.T) {
	dataDirectory := t.TempDir()
	want := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dataDirectory, "api-token"), []byte(want+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := existingAPIToken(serveConfig{DataDir: dataDirectory}, false)
	if err != nil {
		t.Fatal(err)
	}
	if token != want {
		t.Fatalf("token=%q", token)
	}
}

func TestExistingAPITokenUsesEnvironmentToken(t *testing.T) {
	want := "0123456789abcdef"
	token, err := existingAPIToken(serveConfig{APIToken: want, DataDir: t.TempDir()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if token != want {
		t.Fatalf("token=%q", token)
	}
}

func TestExistingAPITokenDoesNotCreateMissingToken(t *testing.T) {
	dataDirectory := t.TempDir()
	if _, err := existingAPIToken(serveConfig{DataDir: dataDirectory}, false); err == nil {
		t.Fatal("missing token was accepted")
	}
	if _, err := os.Stat(filepath.Join(dataDirectory, "api-token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing token was created: %v", err)
	}
}

func TestRotateAPITokenReplacesAndSecuresManagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	old := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(old+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rotated, err := rotateAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) != 48 || rotated == old {
		t.Fatalf("rotated token has unexpected length or value")
	}
	loaded, err := loadAPIToken("", path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != rotated {
		t.Fatal("rotated token file does not contain the returned token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("rotated token mode=%#o", info.Mode().Perm())
	}
}

func TestAPITokenFileRejectsEnvironmentTokenRotation(t *testing.T) {
	_, err := apiTokenFile(serveConfig{
		APIToken: "0123456789abcdef",
		DataDir:  t.TempDir(),
	}, false)
	if err == nil || !strings.Contains(err.Error(), "cannot rotate") {
		t.Fatalf("error=%v", err)
	}
}
