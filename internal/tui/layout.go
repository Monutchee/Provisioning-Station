// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/charmbracelet/x/ansi"
)

type rectangle struct{ x, y, w, h int }

func (r rectangle) contains(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

type action struct {
	rectangle
	label, key string
	value      int
}
type screenLayout struct {
	list, detail rectangle
	actions      []action
	start        int
}

// Rendering and hit testing share this geometry. Terminal coordinates are cells.
func (m Model) layout() screenLayout {
	l := screenLayout{}
	x := 0
	for i, name := range []string{"Artifacts", "Targets", "Provision", "Jobs"} {
		label := "[ " + name + " ]"
		l.actions = append(l.actions, action{rectangle{x, 1, len(label), 1}, label, "tab", i})
		x += len(label) + 1
	}
	x = 0
	add := func(label, key string) {
		text := "[ " + label + " ]"
		l.actions = append(l.actions, action{rectangle{x, 3, len(text), 1}, text, key, 0})
		x += len(text) + 1
	}
	switch m.tab {
	case artifactsTab:
		if m.importing {
			add("Import", "import-submit")
			add("Close", "import-close")
			break
		}
		add("Refresh", "r")
		add("Import", "i")
		add("Raw", "v")
		add("Tree", "t")
		add("Use image", "p")
	case targetsTab:
		add("Discover", "r")
		add("Provision", "enter")
	case jobsTab:
		add("Refresh", "r")
		add("Open logs", "enter")
		add("Cancel opened", "c")
	case provisionTab:
		if m.pending == nil {
			add("Prepare", "submit")
		} else {
			add("Submit retained", "submit")
			add("Reconcile", "r")
		}
	}
	h := max(1, m.height-9)
	l.list = rectangle{0, 6, m.width, h}
	l.detail = rectangle{0, 6, m.width, h}
	if m.tab != provisionTab {
		if m.width >= 100 {
			w := m.width * 2 / 5
			l.list = rectangle{0, 6, w - 2, h}
			l.detail = rectangle{w, 6, m.width - w, h}
		} else {
			lh := max(3, h/3)
			l.list = rectangle{0, 6, m.width, lh}
			l.detail = rectangle{0, 7 + lh, m.width, max(1, h-lh-1)}
		}
	}
	index, count := m.artifactIndex, len(m.artifacts)
	if m.tab == targetsTab {
		index, count = m.targetIndex, len(m.targets)
	}
	if m.tab == jobsTab {
		index, count = m.jobIndex, len(m.jobs)
	}
	l.start = max(0, min(index-l.list.h/2, count-l.list.h))
	return l
}

func artifactSummary(a artifact.StoredArtifact) string {
	meta := a.Manifest.Artifact
	var size int64
	for _, f := range a.Manifest.Files {
		size += f.Size
	}
	return cleanText(fmt.Sprintf("%s\nVersion: %s\nProduct: %s\nMachine: %s\nOperation: %s\nExecutor: %s\nPayload: %d files · %.2f MiB\nCreated: %s\nBuild: %s\nArtifact ID:\n%s\n\nUse Raw for complete metadata or Tree for payload paths.", meta.Name, meta.Version, meta.Product, meta.Machine, meta.Operation, a.Manifest.Executor.Type, len(a.Manifest.Files), float64(size)/(1<<20), meta.CreatedUTC, meta.BuildID, a.ID))
}

func (m Model) View() tea.View {
	if m.width < 70 || m.height < 24 {
		v := tea.NewView(ansi.Truncate("Resize to continue · MPS needs 70×24. Ctrl+C quits; jobs keep running.", max(1, m.width), ""))
		v.AltScreen = true
		return v
	}
	l := m.layout()
	rows := make([]string, m.height)
	put := func(r rectangle, text string) {
		for i, line := range strings.Split(text, "\n") {
			if i >= r.h || r.y+i < 0 || r.y+i >= len(rows) {
				break
			}
			line = ansi.Truncate(line, max(0, min(r.w, m.width-r.x)), "")
			existing := rows[r.y+i]
			left := ansi.Cut(existing, 0, r.x)
			left += strings.Repeat(" ", max(0, r.x-ansi.StringWidth(left)))
			right := ansi.Cut(existing, r.x+r.w, m.width)
			rows[r.y+i] = left + line + strings.Repeat(" ", max(0, r.w-ansi.StringWidth(line))) + right
		}
	}
	put(rectangle{0, 0, m.width, 1}, lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render(fmt.Sprintf("MPS · Provisioning Station   %d artifacts · %d targets · %d jobs", len(m.artifacts), len(m.targets), len(m.jobs))))
	for _, a := range l.actions {
		label := a.label
		if a.key == "tab" && a.value == m.tab {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")).Render(label)
		}
		if a.key == "submit" && m.focus == 8 {
			label = lipgloss.NewStyle().Reverse(true).Render(label)
		}
		if a.key != "tab" && m.busy {
			label = lipgloss.NewStyle().Faint(true).Render(label)
		}
		put(a.rectangle, label)
	}
	switch m.tab {
	case provisionTab:
		put(rectangle{0, 5, m.width, 1}, "PREPARE DEPLOYMENT · Tab focus · ←/→ choose · explicit reset authorization required")
		lines := []string{}
		a, t := "No artifact", "No target"
		if len(m.artifacts) > 0 {
			v := m.artifacts[min(m.artifactIndex, len(m.artifacts)-1)]
			a = cleanLine(v.Manifest.Artifact.Name + " " + v.ID)
		}
		if len(m.targets) > 0 {
			v := m.targets[min(m.targetIndex, len(m.targets)-1)]
			t = cleanLine("cable=" + v.CableSerial + " device=" + v.DeviceIndex + " id=" + v.ID)
		}
		if m.pending != nil {
			a = m.pending.ArtifactID
			t = cleanLine("cable=" + m.pending.TargetCableSerial + " device=" + m.pending.TargetDeviceIndex + " id=" + m.pending.TargetID)
		}
		if m.pending != nil {
			lines = append(lines, "Artifact:", a, "Target: "+t)
		} else {
			for i, line := range []string{"Artifact: " + a, "Target: " + t} {
				if m.focus == i {
					line = "> " + line
				}
				lines = append(lines, line)
			}
		}
		if m.pending == nil {
			for i, f := range m.fields {
				line := f.View()
				if m.focus == i+2 {
					line = "> " + line
				}
				lines = append(lines, line)
			}
			lines = append(lines, "Type exactly: "+cleanLine(m.resetPhrase()), "Prepare freezes this request. Submit requires a second activation.")
		} else {
			lines = append(lines, "Hardware server: "+cleanLine(m.pending.HWServerURL), "TFTP server IPv4: "+cleanLine(m.pending.TFTPServerIP), "Board IPv4: "+cleanLine(m.pending.BoardIP), "Reset authorization retained for selected target", "Retained requestId: "+m.pending.RequestID, "Enter submits/retries exact request; r reconciles.")
			if m.pending.SerialConsole != nil {
				lines = append(lines, "Serial capture: "+cleanLine(m.pending.SerialConsole.PortID))
			}
			if m.pending.DeploymentTasks != nil {
				lines = append(lines, "Deployment tasks: retained in frozen request")
			}
		}
		put(l.detail, strings.Join(lines, "\n"))
	case artifactsTab, targetsTab, jobsTab:
		title := []string{"IMPORTED ARTIFACTS", "DISCOVERED TARGETS", "", "JOB HISTORY"}[m.tab]
		put(rectangle{0, 5, m.width, 1}, title+" · ↑/↓ select · mouse wheel scrolls hovered pane")
		entries := []string{}
		index := m.artifactIndex
		switch m.tab {
		case artifactsTab:
			for _, a := range m.artifacts {
				entries = append(entries, cleanLine(a.Manifest.Artifact.Name+" "+a.Manifest.Artifact.Version+" · "+a.ID))
			}
		case targetsTab:
			index = m.targetIndex
			for _, t := range m.targets {
				entries = append(entries, cleanLine(t.Name+" · "+t.CableSerial+"/"+t.DeviceIndex))
			}
		case jobsTab:
			index = m.jobIndex
			for _, j := range m.jobs {
				entries = append(entries, cleanLine(string(j.State)+" · "+j.ID))
			}
		}
		visible := []string{}
		for i := l.start; i < min(len(entries), l.start+l.list.h); i++ {
			prefix := "  "
			if i == index {
				prefix = "> "
			}
			visible = append(visible, prefix+entries[i])
		}
		if len(entries) == 0 {
			visible = append(visible, "No entries. Refresh to check again.")
		}
		put(l.list, strings.Join(visible, "\n"))
		if m.width >= 100 {
			for y := l.list.y; y < l.list.y+l.list.h; y++ {
				put(rectangle{l.detail.x - 1, y, 1, 1}, lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("│"))
			}
		}
		detail := "Select an entry to inspect."
		switch m.tab {
		case artifactsTab:
			if m.importing {
				detail = m.importPath.View() + "\nEnter imports; Esc closes"
			} else if len(m.artifacts) > 0 {
				if m.treeFocused {
					detail = m.tree.View()
				} else {
					v := m.artifactViewer
					v.SetWidth(l.detail.w)
					v.SetHeight(l.detail.h)
					detail = v.View()
				}
			}
		case targetsTab:
			if len(m.targets) > 0 {
				t := m.targets[min(m.targetIndex, len(m.targets)-1)]
				detail = cleanText(fmt.Sprintf("%s\nCable: %s\nDevice: %s\nTarget ID: %s\nHardware server: %s\nUART: %s", t.Name, t.CableSerial, t.DeviceIndex, t.ID, m.fields[0].Input.Value(), t.SerialAssociation))
				if t.SerialPort != nil {
					detail += "\nMatched serial port ID: " + cleanLine(t.SerialPort.ID)
				}
			}
			lines := strings.Split(detail, "\n")
			detail = strings.Join(lines[min(m.detailOffset, len(lines)):], "\n")
		case jobsTab:
			if m.selectedJob != "" {
				detail = m.jobHeading() + m.logViewer.View()
			} else {
				detail = "Select a job, then Open logs / Enter.\nCancel applies only to the opened job."
			}

		}
		put(l.detail, detail)
	}
	status := cleanLine(m.status)
	if m.busy {
		status = "Working… " + status
	}
	put(rectangle{0, m.height - 2, m.width, 1}, status)
	put(rectangle{0, m.height - 1, m.width, 1}, "Ctrl+C quit · Ctrl+A artifacts · Ctrl+T targets · Ctrl+P provision · Ctrl+J jobs")
	v := tea.NewView(strings.Join(rows, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) mouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.width < 70 || m.height < 24 {
		return m, nil
	}
	l := m.layout()
	for _, a := range l.actions {
		if msg.X < m.width && a.contains(msg.X, msg.Y) {
			if a.key == "tab" {
				if m.importing {
					return m, nil
				}
				m.selectTab(a.value)
				return m, m.focusField()
			}
			if m.busy {
				return m, nil
			}
			if a.key == "import-close" {
				return m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			if a.key == "import-submit" {
				return m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if m.importing {
				return m, nil
			}
			if a.key == "submit" {
				m.focus = 8
				return m, m.provisionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			return m.Update(keyMessage(a.key))
		}
	}
	if m.tab == provisionTab && m.pending == nil && l.detail.contains(msg.X, msg.Y) {
		row := msg.Y - l.detail.y
		if row < 8 {
			m.focus = row
			return m, m.focusField()
		}
		return m, nil
	}
	if m.tab != provisionTab && l.list.contains(msg.X, msg.Y) && !m.importing {
		i := l.start + msg.Y - l.list.y
		switch m.tab {
		case artifactsTab:
			if i < len(m.artifacts) {
				m.artifactIndex = i
				m.updateArtifact()
			}
		case targetsTab:
			if i < len(m.targets) {
				m.targetIndex = i
				m.detailOffset = 0
			}
		case jobsTab:
			if i < len(m.jobs) {
				m.jobIndex = i
			}
		}
	}
	return m, nil
}
func keyMessage(key string) tea.KeyPressMsg {
	if key == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}
func (m Model) mouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.width < 70 || m.height < 24 || m.importing {
		return m, nil
	}
	step := 1
	if msg.Button == tea.MouseWheelUp {
		step = -1
	} else if msg.Button != tea.MouseWheelDown {
		return m, nil
	}
	l := m.layout()
	if m.tab != provisionTab && l.list.contains(msg.X, msg.Y) {
		switch m.tab {
		case artifactsTab:
			m.artifactIndex = max(0, min(len(m.artifacts)-1, m.artifactIndex+step))
			m.updateArtifact()
		case targetsTab:
			m.targetIndex = max(0, min(len(m.targets)-1, m.targetIndex+step))
			m.detailOffset = 0
		case jobsTab:
			m.jobIndex = max(0, min(len(m.jobs)-1, m.jobIndex+step))
		}
		return m, nil
	}
	if l.detail.contains(msg.X, msg.Y) {
		switch m.tab {
		case artifactsTab:
			if m.treeFocused {
				code := tea.KeyDown
				if step < 0 {
					code = tea.KeyUp
				}
				var cmd tea.Cmd
				m.tree.Model, cmd = m.tree.Update(tea.KeyPressMsg{Code: code})
				return m, cmd
			}
			if step > 0 {
				m.artifactViewer.ScrollDown(3)
			} else {
				m.artifactViewer.ScrollUp(3)
			}
		case jobsTab:
			if !m.logArea().contains(msg.X, msg.Y) {
				return m, nil
			}
			if step > 0 {
				m.logViewer.ScrollDown(3)
			} else {
				m.logViewer.ScrollUp(3)
			}
		case targetsTab:
			m.detailOffset = max(0, min(4, m.detailOffset+step))
		}
	}
	return m, nil
}

func (m *Model) selectTab(tab int) {
	m.tab = tab
	m.detailOffset = 0
	m.resize()
	if tab == artifactsTab {
		m.updateArtifact()
	}
	if tab == jobsTab {
		m.logViewer.SetContent(m.logText)
	}
}
func (m Model) jobHeading() string {
	heading := "Opened job: " + cleanLine(m.selectedJob) + "\n"
	for _, j := range m.jobs {
		if j.ID == m.selectedJob {
			if !j.ArtifactAvailable {
				heading += "Artifact unavailable. Reimport the original archive.\n"
			}
			if j.EventsTruncated {
				heading += "Event history truncated.\n"
			}
		}
	}
	return heading
}
func (m Model) logArea() rectangle {
	r := m.layout().detail
	if m.selectedJob != "" {
		n := strings.Count(m.jobHeading(), "\n")
		r.y += n
		r.h = max(1, r.h-n)
	}
	return r
}
