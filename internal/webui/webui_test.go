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
