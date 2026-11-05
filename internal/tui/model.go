// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package tui implements the interactive station client using Bubble Tea MVC.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
	"github.com/Monutchee/Provisioning-Station/internal/tasks"
	"github.com/charmbracelet/x/ansi"
)

const (
	artifactsTab = iota
	targetsTab
	provisionTab
	jobsTab
)

type Model struct {
	ctx                                  context.Context
	client                               *stationclient.Client
	tab, width, height                   int
	artifacts                            []artifact.StoredArtifact
	targets                              []stationclient.Target
	jobs                                 []jobs.Job
	artifactIndex, targetIndex, jobIndex int
	artifactViewer                       Viewer
	logViewer                            Viewer
	tree                                 TreeViewer
	fields                               []TextField
	importPath                           TextField
	importing, treeFocused               bool
	focus                                int
	status                               string
	busy                                 bool
	pending                              *jobs.Request
	pendingAttempted                     bool
	selectedJob                          string
	after                                int
	logText                              string
}
type artifactsMsg struct {
	items []artifact.StoredArtifact
	err   error
}
type targetsMsg struct {
	items       []stationclient.Target
	err         error
	hwServerURL string
}
type jobsMsg struct {
	items []jobs.Job
	err   error
}
type capsMsg struct {
	caps stationclient.Capabilities
	err  error
}
type importedMsg struct {
	artifact artifact.StoredArtifact
	err      error
}
type submittedMsg struct {
	job jobs.Job
	err error
}
type reconciledMsg struct {
	submission stationclient.Submission
	err        error
}
type logsMsg struct {
	id     string
	events []jobs.Event
	err    error
}
type canceledMsg struct {
	job jobs.Job
	err error
}
type tickMsg struct{}

func New(ctx context.Context, client *stationclient.Client) Model {
	return Model{ctx: ctx, client: client, width: 100, height: 30, artifactViewer: NewViewer(), logViewer: NewViewer(), tree: NewTreeViewer(), importPath: NewTextField("Artifact archive path", ""), status: "Loading station…", fields: []TextField{NewTextField("Hardware server", "tcp:127.0.0.1:3121"), NewTextField("TFTP server IPv4", ""), NewTextField("Board IPv4 (optional)", ""), NewTextField("Serial port ID (optional)", ""), NewTextField("Deployment tasks JSON (optional)", ""), NewTextField("Reset confirmation", "")}}
}
func Run(ctx context.Context, client *stationclient.Client) error {
	_, err := tea.NewProgram(New(ctx, client), tea.WithContext(ctx)).Run()
	return err
}
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadArtifacts(), m.loadTargets(), m.loadJobs(), m.loadCapabilities(), tick())
}
func tick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }
func (m Model) loadArtifacts() tea.Cmd {
	return func() tea.Msg { items, err := m.client.ListArtifacts(m.ctx); return artifactsMsg{items, err} }
}
func (m Model) loadTargets() tea.Cmd {
	hw := m.fields[0].Input.Value()
	return func() tea.Msg { items, err := m.client.Targets(m.ctx, hw); return targetsMsg{items, err, hw} }
}
func (m Model) loadJobs() tea.Cmd {
	return func() tea.Msg { items, err := m.client.ListJobs(m.ctx); return jobsMsg{items, err} }
}
func (m Model) loadCapabilities() tea.Cmd {
	return func() tea.Msg { caps, err := m.client.Capabilities(m.ctx); return capsMsg{caps, err} }
}
func (m Model) loadLogs() tea.Cmd {
	id, after := m.selectedJob, m.after
	return func() tea.Msg { events, err := m.client.Events(m.ctx, id, after, 200); return logsMsg{id, events, err} }
}
func (m *Model) setError(err error) bool {
	if err != nil {
		m.status = cleanLine(err.Error())
		return true
	}
	return false
}
func (m *Model) resize() {
	w := max(20, m.width-4)
	h := max(3, m.height-15)
	m.artifactViewer.SetWidth(w)
	m.artifactViewer.SetHeight(h)
	m.logViewer.SetWidth(w)
	m.logViewer.SetHeight(h)
	m.tree.SetSize(w, h)
	for i := range m.fields {
		m.fields[i].Input.SetWidth(max(10, w-32))
	}
	m.importPath.Input.SetWidth(max(10, w-25))
}
func (m *Model) focusField() tea.Cmd {
	for i := range m.fields {
		m.fields[i].Input.Blur()
	}
	if m.focus >= 2 && m.focus < 8 {
		return m.fields[m.focus-2].Input.Focus()
	}
	return nil
}
func (m *Model) updateArtifact() {
	if len(m.artifacts) > 0 {
		m.artifactIndex = min(m.artifactIndex, len(m.artifacts)-1)
		a := m.artifacts[m.artifactIndex]
		m.tree.SetArtifact(a)
		data, _ := json.MarshalIndent(a, "", "  ")
		if m.tab == artifactsTab {
			m.artifactViewer.SetContent(string(data))
		}
	}
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case artifactsMsg:
		if !m.setError(msg.err) {
			m.artifacts = msg.items
			m.updateArtifact()
			m.status = "Artifacts loaded"
		}
		return m, nil
	case targetsMsg:
		if msg.hwServerURL != m.fields[0].Input.Value() {
			return m, nil
		}
		if !m.setError(msg.err) {
			m.targets = msg.items
			m.targetIndex = 0
			m.status = fmt.Sprintf("%d targets discovered", len(msg.items))
		}
		return m, nil
	case jobsMsg:
		if !m.setError(msg.err) {
			m.jobs = msg.items
			m.jobIndex = min(m.jobIndex, max(0, len(m.jobs)-1))
		}
		return m, nil
	case capsMsg:
		if !m.setError(msg.err) {
			if m.fields[1].Input.Value() == "" {
				m.fields[1].Input.SetValue(msg.caps.Network.PreferredTFTPServerIP)
			}
			m.status = "Connected to station " + msg.caps.Version
		}
		return m, nil
	case importedMsg:
		m.busy = false
		if !m.setError(msg.err) {
			m.importing = false
			m.status = "Artifact imported: " + msg.artifact.ID
			return m, m.loadArtifacts()
		}
		return m, nil
	case submittedMsg:
		m.busy = false
		if !m.setError(msg.err) {
			m.pending = nil
			m.pendingAttempted = false
			m.fields[5].Input.SetValue("")
			m.selectedJob = msg.job.ID
			m.after = 0
			m.logText = ""
			m.tab = jobsTab
			m.status = "Accepted job " + msg.job.ID
			return m, tea.Batch(m.loadJobs(), m.loadLogs())
		}
		m.status += "; request retained: retry Submit or press r to reconcile"
		return m, nil
	case reconciledMsg:
		m.busy = false
		if !m.setError(msg.err) {
			m.pending = nil
			m.pendingAttempted = false
			m.fields[5].Input.SetValue("")
			m.selectedJob = msg.submission.JobID
			m.after = 0
			m.logText = ""
			m.tab = jobsTab
			m.status = "Recovered accepted job " + msg.submission.JobID
			return m, tea.Batch(m.loadJobs(), m.loadLogs())
		}
		return m, nil
	case canceledMsg:
		m.busy = false
		if !m.setError(msg.err) {
			m.status = "Cancel requested for " + msg.job.ID
		}
		return m, m.loadJobs()
	case logsMsg:
		if msg.id != m.selectedJob {
			return m, nil
		}
		if !m.setError(msg.err) {
			for _, event := range msg.events {
				if event.Sequence <= m.after {
					continue
				}
				m.logText += fmt.Sprintf("%d %s %s\n", event.Sequence, cleanLine(event.Level), cleanText(event.Message))
				m.after = event.Sequence
			}
			if len(m.logText) > 1<<20 {
				m.logText = m.logText[len(m.logText)-(1<<20):]
			}
			if m.tab == jobsTab {
				m.logViewer.SetContent(m.logText)
				m.logViewer.GotoBottom()
			}
		}
		return m, nil
	case tickMsg:
		if m.tab == jobsTab {
			if m.selectedJob != "" {
				return m, tea.Batch(tick(), m.loadJobs(), m.loadLogs())
			}
			return m, tea.Batch(tick(), m.loadJobs())
		}
		return m, tick()
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.importing {
			if key == "esc" {
				m.importing = false
				m.importPath.Input.Blur()
				return m, nil
			}
			if key == "enter" && !m.busy {
				path := m.importPath.Input.Value()
				m.busy = true
				return m, func() tea.Msg {
					file, err := stationclient.OpenArtifactFile(path)
					if err != nil {
						return importedMsg{err: err}
					}
					defer file.Close()
					info, err := file.Stat()
					if err != nil || !info.Mode().IsRegular() {
						return importedMsg{err: errors.New("artifact must be a regular file")}
					}
					a, err := m.client.ImportArtifact(m.ctx, file)
					return importedMsg{a, err}
				}
			}
			var cmd tea.Cmd
			m.importPath.Input, cmd = m.importPath.Input.Update(msg)
			return m, cmd
		}
		if key == "esc" {
			if m.pending != nil && !m.pendingAttempted {
				m.pending = nil
				m.status = "Prepared submission discarded"
			}
			for i := range m.fields {
				m.fields[i].Input.Blur()
			}
			m.focus = 0
			return m, nil
		}
		if key == "tab" || key == "shift+tab" {
			if m.tab == provisionTab {
				step := 1
				if key == "shift+tab" {
					step = 8
				}
				m.focus = (m.focus + step) % 9
				cmd := m.focusField()
				return m, cmd
			}
			m.tab = (m.tab + 1) % 4
			if m.tab == provisionTab {
				cmd := m.focusField()
				return m, cmd
			}
			if m.tab == artifactsTab {
				m.updateArtifact()
			}
			return m, nil
		}
		// Ctrl+1..4 always select screens even while editing a field.
		switch key {
		case "ctrl+a":
			m.tab = artifactsTab
			m.updateArtifact()
			return m, nil
		case "ctrl+t":
			m.tab = targetsTab
			return m, m.loadTargets()
		case "ctrl+p":
			m.tab = provisionTab
			cmd := m.focusField()
			return m, cmd
		case "ctrl+j":
			m.tab = jobsTab
			m.logViewer.SetContent(m.logText)
			return m, nil
		}
		if m.tab == provisionTab {
			cmd := m.provisionKey(msg)
			return m, cmd
		}
		if key == "q" {
			return m, tea.Quit
		}
		switch m.tab {
		case artifactsTab:
			switch key {
			case "i":
				m.importing = true
				cmd := m.importPath.Input.Focus()
				return m, cmd
			case "r":
				return m, m.loadArtifacts()
			case "up", "k":
				if !m.treeFocused {
					m.artifactIndex = max(0, m.artifactIndex-1)
					m.updateArtifact()
					return m, nil
				}
			case "down", "j":
				if !m.treeFocused {
					m.artifactIndex = min(max(0, len(m.artifacts)-1), m.artifactIndex+1)
					m.updateArtifact()
					return m, nil
				}
			case "t":
				m.treeFocused = !m.treeFocused
				return m, nil
			case "p":
				m.tab = provisionTab
				m.focus = 1
				cmd := m.focusField()
				return m, cmd
			}
			if m.treeFocused {
				var cmd tea.Cmd
				m.tree.Model, cmd = m.tree.Update(msg)
				return m, cmd
			}
			var cmd tea.Cmd
			m.artifactViewer.Model, cmd = m.artifactViewer.Update(msg)
			return m, cmd
		case targetsTab:
			switch key {
			case "r":
				return m, m.loadTargets()
			case "up", "k":
				m.targetIndex = max(0, m.targetIndex-1)
			case "down", "j":
				m.targetIndex = min(max(0, len(m.targets)-1), m.targetIndex+1)
			case "enter", "p":
				m.tab = provisionTab
				m.focus = 2
				m.focusField()
			}
			return m, nil
		case jobsTab:
			switch key {
			case "r":
				return m, m.loadJobs()
			case "up", "k":
				m.jobIndex = max(0, m.jobIndex-1)
				return m, nil
			case "down", "j":
				m.jobIndex = min(max(0, len(m.jobs)-1), m.jobIndex+1)
				return m, nil
			case "enter":
				if len(m.jobs) > 0 {
					m.selectedJob = m.jobs[m.jobIndex].ID
					m.after = 0
					m.logText = ""
					return m, m.loadLogs()
				}
			case "c":
				if !m.busy && m.selectedJob != "" {
					id := m.selectedJob
					m.busy = true
					return m, func() tea.Msg { job, err := m.client.Cancel(m.ctx, id); return canceledMsg{job, err} }
				}
			}
			var cmd tea.Cmd
			m.logViewer.Model, cmd = m.logViewer.Update(msg)
			return m, cmd
		}
	}
	if m.importing && !m.busy {
		var cmd tea.Cmd
		m.importPath.Input, cmd = m.importPath.Input.Update(msg)
		return m, cmd
	}
	if m.tab == provisionTab && m.pending == nil && m.focus >= 2 && m.focus < 8 {
		cmd := m.updateField(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) updateField(msg tea.Msg) tea.Cmd {
	before := m.fields[m.focus-2].Input.Value()
	var cmd tea.Cmd
	m.fields[m.focus-2].Input, cmd = m.fields[m.focus-2].Input.Update(msg)
	if m.focus == 2 && before != m.fields[0].Input.Value() {
		m.targets = nil
		m.targetIndex = 0
		m.fields[5].Input.SetValue("")
		m.status = "Hardware server changed; Ctrl+T discovers targets again"
	}
	return cmd
}

func (m *Model) provisionKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "r" && m.pending != nil && !m.busy {
		id := m.pending.RequestID
		m.busy = true
		return func() tea.Msg { value, err := m.client.Submission(m.ctx, id); return reconciledMsg{value, err} }
	}
	if m.pending != nil {
		if key == "enter" && !m.busy {
			request := *m.pending
			m.pendingAttempted = true
			m.busy = true
			m.status = "Submitting retained request " + request.RequestID
			return func() tea.Msg { job, err := m.client.Submit(m.ctx, request); return submittedMsg{job, err} }
		}
		m.status = "Request frozen: Enter submits/retries the identical request; r reconciles; Esc discards only before first attempt"
		return nil
	}
	if m.focus <= 1 {
		step := 0
		if key == "left" || key == "up" {
			step = -1
		}
		if key == "right" || key == "down" {
			step = 1
		}
		if m.focus == 0 {
			m.artifactIndex = max(0, min(max(0, len(m.artifacts)-1), m.artifactIndex+step))
		} else {
			m.targetIndex = max(0, min(max(0, len(m.targets)-1), m.targetIndex+step))
		}
		return nil
	}
	if m.focus >= 2 && m.focus < 8 {
		return m.updateField(msg)
	}
	if m.focus == 8 && key == "enter" && !m.busy {
		request, err := m.prepareRequest()
		if m.setError(err) {
			return nil
		}
		m.pending = &request
		m.status = "Prepared requestId " + request.RequestID + ". Retained exact request; press Enter again to submit."
		return nil
	}
	return nil
}
func (m Model) resetPhrase() string {
	if len(m.targets) == 0 {
		return "RESET <cable>/<device-index>"
	}
	t := m.targets[m.targetIndex]
	return "RESET " + t.CableSerial + "/" + t.DeviceIndex
}
func (m Model) prepareRequest() (jobs.Request, error) {
	if len(m.artifacts) == 0 || len(m.targets) == 0 {
		return jobs.Request{}, errors.New("select an imported artifact and discovered target")
	}
	target := m.targets[m.targetIndex]
	if target.ID == "" || target.CableSerial == "" || target.DeviceIndex == "" {
		return jobs.Request{}, errors.New("selected target lacks explicit cable/device identity")
	}
	if m.fields[5].Input.Value() != m.resetPhrase() {
		return jobs.Request{}, fmt.Errorf("type exactly %q to authorize resetting this physical target", m.resetPhrase())
	}
	request := jobs.Request{ArtifactID: m.artifacts[m.artifactIndex].ID, HWServerURL: m.fields[0].Input.Value(), TFTPServerIP: m.fields[1].Input.Value(), BoardIP: m.fields[2].Input.Value(), TargetID: target.ID, TargetCableSerial: target.CableSerial, TargetDeviceIndex: target.DeviceIndex}
	if strings.TrimSpace(request.TFTPServerIP) == "" {
		return request, errors.New("TFTP server IPv4 is required")
	}
	if port := strings.TrimSpace(m.fields[3].Input.Value()); port != "" {
		selection := jobs.SerialSelectionManual
		if target.SerialPort != nil && target.SerialPort.ID == port {
			selection = jobs.SerialSelectionMatched
		}
		request.SerialConsole = &jobs.SerialConsoleRequest{PortID: port, Selection: selection}
	}
	if raw := strings.TrimSpace(m.fields[4].Input.Value()); raw != "" {
		var plan tasks.Plan
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return request, fmt.Errorf("deployment tasks JSON: %w", err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return request, errors.New("deployment tasks must contain one JSON object")
		}
		request.DeploymentTasks = &plan
	}
	var err error
	request.RequestID, err = stationclient.NewRequestID()
	return request, err
}
func (m Model) View() tea.View {
	if m.width < 70 || m.height < 24 {
		v := tea.NewView("MPS needs a terminal at least 70 columns × 24 rows. Resize to continue. Ctrl+C quits; jobs keep running.")
		v.AltScreen = true
		return v
	}
	tabs := []string{"Artifacts", "Targets", "Provision", "Jobs"}
	for i := range tabs {
		if i == m.tab {
			tabs[i] = lipgloss.NewStyle().Bold(true).Reverse(true).Render(" " + tabs[i] + " ")
		}
	}
	body := ""
	switch m.tab {
	case artifactsTab:
		body = "i import · ↑/↓ select artifact · t payload tree focus · p provision · r refresh\n"
		if m.importing {
			body += m.importPath.View() + "\nEnter imports; Esc closes\n"
		} else {
			body += m.artifactList() + "\n"
			if m.treeFocused {
				body += m.tree.View()
			} else {
				body += m.artifactViewer.View()
			}
		}
	case targetsTab:
		body = "↑/↓ select target · Enter provision · r discover again\nHardware server: " + m.fields[0].Input.Value() + " (edit on Provision screen)\n"
		start := max(0, m.targetIndex-4)
		end := min(len(m.targets), start+max(1, m.height-12))
		for i := start; i < end; i++ {
			t := m.targets[i]
			marker := "  "
			if i == m.targetIndex {
				marker = "> "
			}
			body += fmt.Sprintf("%s%s  %s  cable=%s device=%s UART=%s\n", marker, cleanLine(t.ID), cleanLine(t.Name), cleanLine(t.CableSerial), cleanLine(t.DeviceIndex), cleanLine(t.SerialAssociation))
			if i == m.targetIndex && t.SerialPort != nil {
				body += "  Matched serial port ID: " + cleanLine(t.SerialPort.ID) + "\n"
			}
		}
		if len(m.targets) == 0 {
			body += "No targets discovered.\n"
		}
	case provisionTab:
		a, t := "No artifact", "No target"
		if len(m.artifacts) > 0 {
			a = cleanLine(m.artifacts[m.artifactIndex].Manifest.Artifact.Name + " " + m.artifacts[m.artifactIndex].ID)
		}
		if len(m.targets) > 0 {
			target := m.targets[m.targetIndex]
			t = cleanLine("cable=" + target.CableSerial + " device=" + target.DeviceIndex + " id=" + target.ID + " " + target.Name)
		}
		if m.pending != nil {
			a = m.pending.ArtifactID
			t = cleanLine("cable=" + m.pending.TargetCableSerial + " device=" + m.pending.TargetDeviceIndex + " id=" + m.pending.TargetID)
		}
		body = "Tab/Shift+Tab focus · ←/→ choose artifact/target · Ctrl+T discover · Esc leave field\n"
		for i, line := range []string{"Artifact: " + a, "Target: " + t} {
			if m.focus == i {
				line = lipgloss.NewStyle().Reverse(true).Render(line)
			}
			body += line + "\n"
		}
		if m.pending == nil {
			for _, field := range m.fields {
				body += field.View() + "\n"
			}
		} else {
			body += "Hardware server: " + cleanLine(m.pending.HWServerURL) + "\nTFTP server IPv4: " + cleanLine(m.pending.TFTPServerIP) + "\nBoard IPv4: " + cleanLine(m.pending.BoardIP) + "\n"
			serial := "none"
			if m.pending.SerialConsole != nil {
				data, _ := json.Marshal(m.pending.SerialConsole)
				serial = string(data)
			}
			body += "Serial capture: " + serial + "\n"
			taskJSON := "none"
			if m.pending.DeploymentTasks != nil {
				data, _ := json.Marshal(m.pending.DeploymentTasks)
				taskJSON = string(data)
			}
			body += "Deployment tasks: " + taskJSON + "\nReset authorization retained for selected target\n"
		}
		if m.pending == nil {
			body += "Type this exact reset confirmation:\n" + cleanLine(m.resetPhrase()) + "\n"
		}
		body += (Button{Label: "Prepare / Submit", Enabled: !m.busy, Focused: m.focus == 8}).View() + "\n"
		if m.pending != nil {
			body += "Retained requestId: " + m.pending.RequestID + "\nEnter submits/retries exact request; r reconciles. Target: " + cleanLine(m.pending.TargetCableSerial+"/"+m.pending.TargetDeviceIndex) + "\n"
		}
	case jobsTab:
		body = "↑/↓ select · Enter inspect/follow logs · c cancel opened job · r refresh\n"
		start := max(0, m.jobIndex-3)
		end := min(len(m.jobs), start+7)
		for i := start; i < end; i++ {
			job := m.jobs[i]
			marker := "  "
			if i == m.jobIndex {
				marker = "> "
			}
			body += fmt.Sprintf("%s%s  %-10s %s\n", marker, cleanLine(job.ID), cleanLine(string(job.State)), cleanLine(job.Request.TargetCableSerial))
		}
		if len(m.jobs) == 0 {
			body += "No jobs.\n"
		}
		if m.selectedJob != "" {
			body += "Opened job: " + cleanLine(m.selectedJob) + "\n" + m.logViewer.View()
		}
	}
	footer := "\n" + cleanLine(m.status) + "\nCtrl+A artifacts · Ctrl+T targets · Ctrl+P provision · Ctrl+J jobs · Ctrl+C quit"
	if m.busy {
		footer = "\nWorking… " + cleanLine(m.status) + "\nCtrl+C leaves durable jobs running"
	}
	content := strings.Join(tabs, "  ") + "\n\n" + body + footer
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	content = strings.Join(lines, "\n")
	view := tea.NewView(lipgloss.NewStyle().Width(max(20, m.width)).MaxHeight(max(5, m.height)).Render(content))
	view.AltScreen = true
	return view
}
func (m Model) artifactList() string {
	if len(m.artifacts) == 0 {
		return "No imported artifacts."
	}
	lines := []string{}
	start := max(0, m.artifactIndex-2)
	for i := start; i < min(len(m.artifacts), start+5); i++ {
		a := m.artifacts[i]
		prefix := "  "
		if i == m.artifactIndex {
			prefix = "> "
		}
		lines = append(lines, prefix+cleanLine(a.Manifest.Artifact.Name+" "+a.ID))
	}
	return strings.Join(lines, "\n")
}

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 && (r < 127 || r > 159) {
			return r
		}
		return -1
	}, ansi.Strip(s))
}
func cleanLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(cleanText(s))
}
