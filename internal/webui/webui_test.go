// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerEmbedsTerminalAssets(t *testing.T) {
	handler := Handler()
	for _, test := range []struct {
		path    string
		content string
	}{
		{path: "/", content: "Serial console"},
		{path: "/vendor/xterm/xterm.js", content: "Terminal"},
		{path: "/vendor/xterm/addon-fit.js", content: "FitAddon"},
		{path: "/vendor/xterm/xterm.css", content: ".xterm"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.content) {
			t.Errorf("GET %s: status=%d expected content %q", test.path, response.Code, test.content)
		}
	}
}

func TestHandlerEmbedsOptionalManualSerialSelection(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{
		"No serial capture (JTAG only)",
		"targetRequest.serialConsole.selection = \"manual\"",
		"No automatic UART match; select a port or use JTAG only",
	} {
		if response.Code != http.StatusOK || !strings.Contains(body, expected) {
			t.Errorf("GET /app.js: status=%d expected content %q", response.Code, expected)
		}
	}
	for _, forbidden := range []string{
		"checkbox.disabled = !serialAvailable",
		"has no safely associated local FTDI UART",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("GET /app.js still contains UART gating %q", forbidden)
		}
	}
}

func TestHandlerCSPAllowsXtermDynamicStylesButNotInlineScripts(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") ||
		!strings.Contains(policy, "script-src 'self'") || strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("content security policy = %q", policy)
	}
}
