// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/servicecontrol"
	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
)

type trip func(*http.Request) (*http.Response, error)

func (f trip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"MPS_URL", "MPS_TOKEN", "MPS_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
}
func readyResponse() *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"apiVersion":"v1","artifact":{"schema":"` + artifact.SchemaName + `"}}`))}
}
func TestNoninteractiveHelpAndVersionNeverContactService(t *testing.T) {
	clearEnv(t)
	for _, args := range [][]string{nil, {"version"}, {"help"}} {
		var out bytes.Buffer
		calls := 0
		err := run(context.Background(), args, &out, &out, dependencies{http: &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) { calls++; return nil, syscall.ECONNREFUSED })}, start: func(context.Context) error { calls++; return nil }})
		if err != nil || calls != 0 || out.Len() == 0 {
			t.Fatal(err, calls, out.String())
		}
	}
}
func TestOnlyTUIAutostartsRefusedDefaultService(t *testing.T) {
	clearEnv(t)
	for _, command := range []string{"tui", "status"} {
		t.Run(command, func(t *testing.T) {
			started, launched := 0, 0
			dep := dependencies{http: &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
				if started == 0 {
					return nil, syscall.ECONNREFUSED
				}
				return readyResponse(), nil
			})}, start: func(context.Context) error { started++; return nil }, launchTUI: func(context.Context, *stationclient.Client) error { launched++; return nil }}
			err := run(context.Background(), []string{command}, io.Discard, io.Discard, dep)
			if command == "tui" {
				if err != nil || started != 1 || launched != 1 {
					t.Fatal(err, started, launched)
				}
			} else if err == nil || started != 0 || launched != 0 {
				t.Fatal(err, started, launched)
			}
		})
	}
}
func TestExplicitServiceControlAndTokenConfiguration(t *testing.T) {
	clearEnv(t)
	started, stopped := 0, 0
	dep := dependencies{start: func(context.Context) error { started++; return nil }, stop: func(context.Context) error { stopped++; return nil }, query: func(context.Context) (servicecontrol.Status, error) {
		return servicecontrol.Status{State: "active"}, nil
	}}
	var out bytes.Buffer
	for _, action := range []string{"start", "stop", "status"} {
		if err := run(context.Background(), []string{"service", action}, &out, &out, dep); err != nil {
			t.Fatal(err)
		}
	}
	if started != 1 || stopped != 1 || !strings.Contains(out.String(), "active") {
		t.Fatal(out.String())
	}
	t.Setenv("MPS_TOKEN", "secret")
	t.Setenv("MPS_TOKEN_FILE", "unreadable")
	if err := run(context.Background(), []string{"status"}, io.Discard, io.Discard, dep); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
func TestUncertainSubmissionRetainsPrintedKeyAndExactRetry(t *testing.T) {
	clearEnv(t)
	request := jobs.Request{ArtifactID: strings.Repeat("a", 64), HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.2", TargetID: "2", TargetCableSerial: "BOARD-A", TargetDeviceIndex: "0"}
	data, _ := json.Marshal(request)
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	requests := []jobs.Request{}
	dep := dependencies{http: &http.Client{Transport: trip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/capabilities" {
			return readyResponse(), nil
		}
		if r.URL.Path != "/api/v1/jobs" {
			t.Fatal(r.URL)
		}
		var req jobs.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, req)
		return nil, errors.New("reply lost after acceptance")
	})}}
	var out, errOut bytes.Buffer
	args := []string{"job", "submit", "--request", path, "--acknowledge-reset"}
	err := run(context.Background(), args, &out, &errOut, dep)
	if err == nil || len(requests) != 1 || requests[0].RequestID == "" {
		t.Fatal(err, requests)
	}
	key := requests[0].RequestID
	if !strings.Contains(errOut.String(), key) || !strings.Contains(err.Error(), "job reconcile "+key) {
		t.Fatal(errOut.String(), err)
	}
	args = append(args, "--request-id", key)
	_ = run(context.Background(), args, &out, &errOut, dep)
	if len(requests) != 2 {
		t.Fatal(requests)
	}
	a, _ := json.Marshal(requests[0])
	b, _ := json.Marshal(requests[1])
	if !bytes.Equal(a, b) {
		t.Fatal("retry changed request")
	}
	before := len(requests)
	_ = run(context.Background(), []string{"job", "submit", "--request", path}, &out, &errOut, dep)
	if len(requests) != before {
		t.Fatal("unacknowledged reset submitted")
	}
}
func TestBoundedJobLogCursor(t *testing.T) {
	clearEnv(t)
	id := strings.Repeat("a", 32)
	dep := dependencies{http: &http.Client{Transport: trip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/capabilities" {
			return readyResponse(), nil
		}
		if r.URL.Query().Get("after") != "5" || r.URL.Query().Get("limit") != "2" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"events":[{"sequence":6,"message":"ok"}]}`))}, nil
	})}}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"job", "logs", "--after", "5", "--limit", "2", id}, &out, &out, dep); err != nil || !strings.Contains(out.String(), `"sequence": 6`) {
		t.Fatal(err, out.String())
	}
}

func TestStorageCleanupRequiresExplicitMode(t *testing.T) {
	for _, args := range [][]string{{"cleanup"}, {"cleanup", "--apply", "--dry-run"}, {"cleanup", "--apply", "extra"}} {
		calls := 0
		client, err := stationclient.New(stationclient.Config{URL: "http://127.0.0.1:8042", HTTPClient: &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) { calls++; return readyResponse(), nil })}})
		if err != nil {
			t.Fatal(err)
		}
		if err := storageCommand(context.Background(), client, args, io.Discard, io.Discard); err == nil || calls != 0 {
			t.Fatal(args, err, calls)
		}
	}
	for _, mode := range []string{"--dry-run", "--apply"} {
		clearEnv(t)
		var out bytes.Buffer
		calls := 0
		dep := dependencies{http: &http.Client{Transport: trip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/v1/capabilities" {
				return readyResponse(), nil
			}
			calls++
			data, _ := io.ReadAll(r.Body)
			expected := `{"apply":false}`
			if mode == "--apply" {
				expected = `{"apply":true}`
			}
			if r.Method != "POST" || r.URL.Path != "/api/v1/storage/cleanup" || string(data) != expected {
				t.Fatal(r.Method, r.URL, string(data))
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"apply":false,"reclaimableBytes":12}`))}, nil
		})}}
		if err := run(context.Background(), []string{"storage", "cleanup", mode}, &out, &out, dep); err != nil || calls != 1 || !strings.Contains(out.String(), "reclaimableBytes") {
			t.Fatal(err, calls, out.String())
		}
	}
}
