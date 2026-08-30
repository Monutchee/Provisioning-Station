// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
