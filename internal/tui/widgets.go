// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/tree"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

// Viewer, TreeViewer and TextField reuse upstream interaction and rendering.
// Button is local because Bubbles has no button component.
type Viewer struct{ viewport.Model }

func NewViewer() Viewer { return Viewer{viewport.New(viewport.WithWidth(80), viewport.WithHeight(15))} }

type TreeViewer struct{ tree.Model }

func NewTreeViewer() TreeViewer {
	m := tree.New(tree.Root("Artifact payload"), 80, 15)
	m.SetShowHelp(false)
	return TreeViewer{m}
}
func (t *TreeViewer) SetArtifact(a artifact.StoredArtifact) {
	root := tree.Root(cleanLine(a.Manifest.Artifact.Name))
	paths := make([]string, 0, len(a.Manifest.Files))
	for path := range a.Manifest.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	nodes := map[string]*tree.Node{"": root}
	for _, path := range paths {
		parts := strings.Split(path, "/")
		parent := ""
		for i, part := range parts {
			key := strings.Join(parts[:i+1], "/")
			if _, ok := nodes[key]; !ok {
				label := cleanLine(part)
				if i == len(parts)-1 {
					f := a.Manifest.Files[path]
					label = fmt.Sprintf("%s (%d bytes, %s)", cleanLine(part), f.Size, cleanLine(f.Mode))
				}
				node := tree.Root(label)
				nodes[parent].Child(node)
				nodes[key] = node
			}
			parent = key
		}
	}
	t.SetNodes(root)
}

type TextField struct {
	Label string
	Input textinput.Model
}

func NewTextField(label, value string) TextField {
	input := textinput.New()
	input.SetValue(value)
	input.CharLimit = 1 << 16
	input.SetWidth(64)
	return TextField{Label: label, Input: input}
}
func (f TextField) View() string { return f.Label + ": " + f.Input.View() }

type Button struct {
	Label            string
	Enabled, Focused bool
}

func (b Button) View() string {
	style := lipgloss.NewStyle().Padding(0, 1)
	if !b.Enabled {
		style = style.Faint(true)
	} else if b.Focused {
		style = style.Bold(true).Reverse(true)
	}
	return style.Render("[ " + b.Label + " ]")
}
