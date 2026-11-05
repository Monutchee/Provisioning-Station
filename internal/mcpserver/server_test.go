// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func makeServer(t *testing.T, root string, calls *atomic.Int32) *Server {
	t.Helper()
	s, err := New(Config{ImportRoot: root, MaxImportBytes: 16, Station: stationclient.Config{URL: "http://127.0.0.1:9999", Token: "host-secret", HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer host-secret" {
			t.Error("missing host token")
		}
		body := `{"id":"job"}`
		if strings.HasSuffix(r.URL.Path, "/events") {
			body = `{"events":[{"sequence":6,"message":"done"}]}`
		}
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func session(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLocalOnlyAndImportAbsent(t *testing.T) {
	if _, err := New(Config{Station: stationclient.Config{URL: "http://192.0.2.1:9000"}}); err == nil {
		t.Fatal("remote station accepted")
	}
	var calls atomic.Int32
	s := makeServer(t, "", &calls)
	cs := session(t, s)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	foundSubmit := false
	for _, tool := range list.Tools {
		if tool.Name == "import_artifact" {
			t.Fatal("import tool exposed without root")
		}
		if tool.Name == "submit_job" {
			foundSubmit = true
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint || !tool.Annotations.IdempotentHint {
				t.Fatal("missing submit annotations")
			}
		}
		data, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(data), "host-secret") || strings.Contains(string(data), `"token"`) {
			t.Fatal("host credentials exposed")
		}
	}
	if !foundSubmit {
		t.Fatal("submit tool missing")
	}
}
func TestSubmitSafetyAndBoundedObservation(t *testing.T) {
	var calls atomic.Int32
	s := makeServer(t, "", &calls)
	cs := session(t, s)
	key := strings.Repeat("a", 32)
	request := map[string]any{"requestId": key, "artifactId": strings.Repeat("b", 64), "hwServerUrl": "tcp:127.0.0.1:3121", "tftpServerIp": "192.0.2.2", "targetId": "2", "targetCableSerial": "serial", "targetDeviceIndex": "0"}
	for _, args := range []any{map[string]any{"request": request, "acknowledgeReset": false}, map[string]any{"request": map[string]any{"requestId": key, "artifactId": strings.Repeat("b", 64), "hwServerUrl": "tcp:127.0.0.1:3121", "tftpServerIp": "192.0.2.2"}, "acknowledgeReset": true}} {
		if res := call(t, cs, "submit_job", args); !res.IsError {
			t.Fatal("unsafe submit allowed", res)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe request reached HTTP")
	}
	if res := call(t, cs, "submit_job", map[string]any{"request": request, "acknowledgeReset": true}); res.IsError {
		t.Fatal(res)
	}
	before := calls.Load()
	if res := call(t, cs, "job_events", map[string]any{"jobId": key, "after": 0, "limit": 1001}); !res.IsError {
		t.Fatal("unbounded read accepted")
	}
	if calls.Load() != before {
		t.Fatal("invalid cursor reached HTTP")
	}
	res := call(t, cs, "job_events", map[string]any{"jobId": key, "after": 5, "limit": 2})
	if res.IsError {
		t.Fatal(res)
	}
	data, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(data), `"nextAfter":6`) {
		t.Fatal(string(data))
	}
	before = calls.Load()
	_ = cs.Close()
	if calls.Load() != before {
		t.Fatal("disconnect canceled durable job")
	}
}
func TestRootedImports(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "good"), []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 17)), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := makeServer(t, root, &calls)
	cs := session(t, s)
	paths := []string{"../outside", outside, "a/../good", "large", "missing", "."}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, "escape")
	}
	for _, path := range paths {
		if res := call(t, cs, "import_artifact", map[string]any{"path": path}); !res.IsError {
			t.Errorf("accepted %q", path)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected files uploaded")
	}
	if res := call(t, cs, "import_artifact", map[string]any{"path": "good"}); res.IsError {
		t.Fatal(res)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
