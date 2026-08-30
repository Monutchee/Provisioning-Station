// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

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
