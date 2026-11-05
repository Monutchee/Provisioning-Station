// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package tui

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/charmbracelet/x/ansi"
)

func clickAction(t *testing.T, m Model, key string, value int) (Model, tea.Cmd) {
	t.Helper()
	for _, a := range m.layout().actions {
		if a.key == key && (key != "tab" || a.value == value) {
			next, cmd := m.Update(tea.MouseClickMsg{X: a.x, Y: a.y, Button: tea.MouseLeft})
			return next.(Model), cmd
		}
	}
	t.Fatalf("missing action %s", key)
	return m, nil
}
func TestFullTerminalLayoutBounds(t *testing.T) {
	for _, size := range [][2]int{{70, 24}, {80, 24}, {100, 30}, {160, 50}} {
		for tab := 0; tab < 4; tab++ {
			t.Run(fmt.Sprint(size, tab), func(t *testing.T) {
				m := configured(t, nil)
				m.width = size[0]
				m.height = size[1]
				m.tab = tab
				m.status = "connection-status"
				m.resize()
				m.updateArtifact()
				m.artifacts[0].Manifest.Artifact.Name = strings.Repeat("wide界", 100)
				m.jobs = []jobs.Job{{ID: strings.Repeat("j", 64)}}
				v := m.View()
				lines := strings.Split(v.Content, "\n")
				if len(lines) != size[1] {
					t.Fatalf("height %d", len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("overflow: %q", line)
					}
				}
				if !strings.Contains(lines[len(lines)-2], "connection-status") || !strings.Contains(lines[len(lines)-1], "Ctrl+C quit") {
					t.Fatal("footer hidden")
				}
				if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
					t.Fatal("terminal modes missing")
				}
			})
		}
	}
}
func TestMouseNavigationAndHoveredScrolling(t *testing.T) {
	m := configured(t, nil)
	for i := 0; i < 40; i++ {
		m.artifacts = append(m.artifacts, artifact.StoredArtifact{ID: fmt.Sprint(i)})
	}
	m.updateArtifact()
	m.resize()
	l := m.layout()
	for i := 0; i < 45; i++ {
		next, _ := m.Update(tea.MouseWheelMsg{X: l.list.x, Y: l.list.y, Button: tea.MouseWheelDown})
		m = next.(Model)
	}
	if m.artifactIndex != len(m.artifacts)-1 {
		t.Fatal("last item unreachable")
	}
	l = m.layout()
	next, _ := m.Update(tea.MouseClickMsg{X: l.list.x, Y: l.list.y, Button: tea.MouseLeft})
	m = next.(Model)
	if m.artifactIndex != l.start {
		t.Fatal("row geometry mismatch")
	}
	index := m.artifactIndex
	m.artifactViewer.SetContent(strings.Repeat("detail line\n", 100))
	next, _ = m.Update(tea.MouseWheelMsg{X: l.detail.x, Y: l.detail.y, Button: tea.MouseWheelDown})
	m = next.(Model)
	if m.artifactViewer.YOffset() == 0 {
		t.Fatal("detail wheel did not scroll")
	}
	if m.artifactIndex != index {
		t.Fatal("detail wheel changed selection")
	}
	m, _ = clickAction(t, m, "tab", jobsTab)
	if m.tab != jobsTab {
		t.Fatal("tab click failed")
	}
	m, _ = clickAction(t, m, "tab", provisionTab)
	l = m.layout()
	next, cmd := m.Update(tea.MouseClickMsg{X: l.detail.x, Y: l.detail.y + 3, Button: tea.MouseLeft})
	m = next.(Model)
	if m.focus != 3 || cmd == nil {
		t.Fatal("field focus failed")
	}
}
func TestMouseSubmissionKeepsTwoStepAuthorization(t *testing.T) {
	m := configured(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })
	m.tab = provisionTab
	m, cmd := clickAction(t, m, "submit", 0)
	if cmd != nil || m.pending != nil {
		t.Fatal("missing authorization prepared")
	}
	m.fields[5].Input.SetValue(m.resetPhrase())
	m, cmd = clickAction(t, m, "submit", 0)
	if cmd != nil || m.pending == nil {
		t.Fatal("prepare submitted or missing request")
	}
	frozen := *m.pending
	m.fields[1].Input.SetValue("192.0.2.99")
	m, cmd = clickAction(t, m, "submit", 0)
	if cmd == nil || !m.busy || !m.pendingAttempted || m.pending.RequestID != frozen.RequestID || m.pending.TFTPServerIP != frozen.TFTPServerIP {
		t.Fatal("submission did not freeze request")
	}
	m, cmd = clickAction(t, m, "submit", 0)
	if cmd != nil {
		t.Fatal("busy click duplicated submission")
	}
}
func TestSummaryAndExplicitRawTree(t *testing.T) {
	m := configured(t, nil)
	m.updateArtifact()
	if strings.Contains(m.artifactViewer.View(), `"manifest"`) || !strings.Contains(m.artifactViewer.View(), "Product:") {
		t.Fatal("default is not summary")
	}
	m, _ = clickAction(t, m, "v", 0)
	if !m.rawArtifact || !strings.Contains(m.artifactViewer.View(), `"manifest"`) {
		t.Fatal("raw metadata unavailable")
	}
	m, _ = clickAction(t, m, "t", 0)
	if !m.treeFocused {
		t.Fatal("tree unavailable")
	}
}

func TestActionBoundsAndImportControls(t *testing.T) {
	m := configured(t, nil)
	m.width = 70
	m.height = 24
	m.resize()
	for tab := 0; tab < 4; tab++ {
		m.selectTab(tab)
		for _, a := range m.layout().actions {
			if a.x+a.w > m.width {
				t.Fatalf("clipped %s", a.label)
			}
		}
	}
	m.selectTab(artifactsTab)
	m, _ = clickAction(t, m, "i", 0)
	if !m.importing {
		t.Fatal("import did not open")
	}
	if m.importPath.Input.Width() > m.layout().detail.w-20 {
		t.Fatal("import input exceeds detail pane")
	}
	m, _ = clickAction(t, m, "import-close", 0)
	if m.importing {
		t.Fatal("import close did not work")
	}
}
func TestMouseJobTabAndLogGeometry(t *testing.T) {
	m := configured(t, nil)
	m.width = 70
	m.height = 24
	m.selectedJob = "opened"
	m.jobs = []jobs.Job{{ID: "opened", EventsTruncated: true}}
	m.logText = strings.Repeat("line\n", 80) + "FINAL EVENT"
	m, _ = clickAction(t, m, "tab", jobsTab)
	if m.logViewer.Height() != m.logArea().h {
		t.Fatal("stored log height differs from rendered area")
	}
	m.logViewer.GotoBottom()
	if !strings.Contains(m.View().Content, "FINAL EVENT") {
		t.Fatal("final log event unreachable")
	}
	m.logViewer.GotoTop()
	next, _ := m.Update(logsMsg{id: "opened"})
	m = next.(Model)
	if m.logViewer.AtBottom() {
		t.Fatal("empty poll reset manual scroll")
	}
	r := m.logArea()
	index := m.jobIndex
	next, _ = m.Update(tea.MouseWheelMsg{X: r.x, Y: r.y, Button: tea.MouseWheelDown})
	m = next.(Model)
	if m.logViewer.YOffset() == 0 || m.jobIndex != index {
		t.Fatal("log wheel did not stay in log pane")
	}
}
