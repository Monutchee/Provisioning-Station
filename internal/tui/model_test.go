// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

type trip func(*http.Request) (*http.Response, error)

func (f trip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func configured(t *testing.T, f trip) Model {
	t.Helper()
	c, err := stationclient.New(stationclient.Config{URL: "http://127.0.0.1:8042", HTTPClient: &http.Client{Transport: f}})
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), c)
	m.artifacts = []artifact.StoredArtifact{{ID: strings.Repeat("a", 64), Manifest: artifact.Manifest{Artifact: artifact.ArtifactMetadata{Name: "image"}}}}
	m.targets = []stationclient.Target{{Target: xsdb.Target{ID: "2", Name: "board", CableSerial: "BOARD-A", DeviceIndex: "0"}}}
	m.fields[1].Input.SetValue("192.0.2.2")
	return m
}
func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func TestResetAcknowledgmentAndPendingRequestRetention(t *testing.T) {
	requests := []jobs.Request{}
	m := configured(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/jobs" {
			t.Fatal(r.URL)
		}
		var request jobs.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
		return nil, errors.New("connection lost after acceptance")
	})
	m.tab = provisionTab
	m.focus = 8
	if cmd := m.provisionKey(enter()); cmd != nil || m.pending != nil {
		t.Fatal("unacknowledged reset prepared")
	}
	m.fields[5].Input.SetValue(m.resetPhrase())
	if cmd := m.provisionKey(enter()); cmd != nil || m.pending == nil {
		t.Fatal("not retained before send")
	}
	key := m.pending.RequestID
	if len(requests) != 0 {
		t.Fatal("first activation submitted")
	}
	if !strings.Contains(m.View().Content, key) {
		t.Fatal("request key not displayed")
	}
	cmd := m.provisionKey(enter())
	if cmd == nil {
		t.Fatal("no submit command")
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(Model)
	if m.pending == nil || m.pending.RequestID != key {
		t.Fatal("uncertain request lost")
	}
	m.fields[1].Input.SetValue("192.0.2.99")
	cmd = m.provisionKey(enter())
	_ = cmd()
	if len(requests) != 2 || requests[0].RequestID != requests[1].RequestID || requests[0].TFTPServerIP != requests[1].TFTPServerIP {
		t.Fatal("retry changed payload", requests)
	}
	_, quit := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if quit == nil {
		t.Fatal("quit missing")
	}
	if len(requests) != 2 {
		t.Fatal("quit touched durable job")
	}
}
func TestTasksRejectTrailingJSONAndTargetChanges(t *testing.T) {
	m := configured(t, nil)
	m.fields[5].Input.SetValue(m.resetPhrase())
	m.fields[4].Input.SetValue(`{"version":1} {}`)
	if _, err := m.prepareRequest(); err == nil {
		t.Fatal("trailing tasks accepted")
	}
	m.fields[4].Input.SetValue(`{"version":1}`)
	if _, err := m.prepareRequest(); err != nil {
		t.Fatal(err)
	}
	m.focus = 2
	m.fields[0].Input.Focus()
	m.provisionKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if len(m.targets) != 0 {
		t.Fatal("old server targets retained")
	}
}
func TestViewerIsolationAndTerminalSanitization(t *testing.T) {
	m := configured(t, nil)
	m.tab = jobsTab
	m.selectedJob = strings.Repeat("a", 32)
	m.logText = "log-only"
	m.logViewer.SetContent(m.logText)
	updated, _ := m.Update(artifactsMsg{items: m.artifacts})
	m = updated.(Model)
	if !strings.Contains(m.logViewer.View(), "log-only") {
		t.Fatal("artifact update overwrote logs")
	}
	m.tab = artifactsTab
	m.updateArtifact()
	if strings.Contains(m.artifactViewer.View(), "log-only") {
		t.Fatal("logs overwrote artifact viewer")
	}
	hostile := "board\x1b]52;c;YWJj\a\x1b[31mred\r\x00"
	if got := cleanLine(hostile); strings.ContainsAny(got, "\x1b\a\r\x00") || strings.Contains(got, "YWJj") {
		t.Fatal(got)
	}
	m.targets[0].Name = hostile
	m.targets[0].CableSerial = hostile
	m.tab = targetsTab
	v := m.View().Content
	if strings.Contains(v, "\x1b]52") || strings.Contains(v, "YWJj") {
		t.Fatal("terminal escape leaked")
	}
	m.width = 40
	m.height = 12
	if !strings.Contains(m.View().Content, "Resize to continue") {
		t.Fatal("no small terminal guidance")
	}
}
func TestAllJobsPollsWithoutOpenedJobAndResize(t *testing.T) {
	calls := 0
	m := configured(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"jobs":[]}`))}, nil
	})
	m.tab = jobsTab
	_, cmd := m.Update(tickMsg{})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatal("all jobs not polled")
	}
	_ = batch[1]()
	if calls != 1 {
		t.Fatal(calls)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	if m.width != 80 || m.height != 24 {
		t.Fatal("resize lost")
	}
}

func TestStaleTargetDiscoveryIsIgnored(t *testing.T) {
	m := configured(t, nil)
	old := m.fields[0].Input.Value()
	m.fields[0].Input.SetValue("tcp:127.0.0.1:4444")
	m.targets = nil
	updated, _ := m.Update(targetsMsg{items: []stationclient.Target{{Target: xsdb.Target{ID: "stale"}}}, hwServerURL: old})
	m = updated.(Model)
	if len(m.targets) != 0 {
		t.Fatal("old server target restored")
	}
}
func TestPendingViewShowsFrozenIdentityAfterSelectionChanges(t *testing.T) {
	m := configured(t, nil)
	m.tab = provisionTab
	m.focus = 8
	m.fields[5].Input.SetValue(m.resetPhrase())
	m.provisionKey(enter())
	if m.pending == nil {
		t.Fatal("not prepared")
	}
	m.targets = append(m.targets, stationclient.Target{Target: xsdb.Target{ID: "99", Name: "different-target", CableSerial: "OTHER", DeviceIndex: "7"}})
	m.targetIndex = 1
	m.artifacts = append(m.artifacts, artifact.StoredArtifact{ID: strings.Repeat("b", 64)})
	m.artifactIndex = 1
	m.fields[0].Input.SetValue("tcp:new-server:9999")
	m.fields[1].Input.SetValue("192.0.2.99")
	view := m.View().Content
	for _, want := range []string{"BOARD-A", "tcp:127.0.0.1:3121", "192.0.2.2", strings.Repeat("a", 64)} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing frozen %q: %s", want, view)
		}
	}
	for _, unwanted := range []string{"OTHER", "tcp:new-server", "192.0.2.99"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("live value shown: %s", view)
		}
	}
}

func TestPasteRoutesToFocusedInputsAndInvalidatesDiscovery(t *testing.T) {
	m := configured(t, nil)
	m.tab = provisionTab
	m.focus = 2
	if cmd := m.focusField(); cmd == nil {
		t.Fatal("cursor command discarded")
	}
	updated, _ := m.Update(tea.PasteMsg{Content: "new-server"})
	m = updated.(Model)
	if !strings.Contains(m.fields[0].Input.Value(), "new-server") || len(m.targets) != 0 {
		t.Fatal("paste not routed or old targets retained")
	}
	m.importing = true
	m.importPath.Input.Focus()
	updated, _ = m.Update(tea.PasteMsg{Content: "/tmp/archive.tar.gz"})
	m = updated.(Model)
	if m.importPath.Input.Value() != "/tmp/archive.tar.gz" {
		t.Fatal("import paste dropped")
	}
}
