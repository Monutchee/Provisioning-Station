// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDescriptorsValidateAndRemainImmutable(t *testing.T) {
	descriptor := Descriptor{ID: "example.prepare", TimeoutSeconds: 1, Parameters: []Parameter{
		{Name: "mode", Type: "string", Required: true, Enum: []any{"test"}},
		{Name: "count", Type: "integer"}, {Name: "ratio", Type: "number"}, {Name: "enabled", Type: "boolean"},
	}}
	registry, err := New(Definition{Descriptor: descriptor, Run: func(context.Context, Context, map[string]any, func(string, string)) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Parameters[0].Enum[0] = "mutated"
	invocation := Invocation{ID: "prepare", Task: "example.prepare", Parameters: map[string]any{"mode": "test", "count": 2.0, "ratio": 0.5, "enabled": true}}
	plan := &Plan{Version: 1, BeforeBoot: []Invocation{invocation}}
	if err := registry.Validate(plan); err != nil {
		t.Fatal(err)
	}
	exposed := registry.Capabilities()
	exposed.Tasks[0].Parameters[0].Enum[0] = "also-mutated"
	if err := registry.Validate(plan); err != nil {
		t.Fatal(err)
	}
	for name, params := range map[string]map[string]any{
		"required": {}, "enum": {"mode": "other"}, "unknown": {"mode": "test", "command": "no"},
		"integer": {"mode": "test", "count": 1.5}, "boolean": {"mode": "test", "enabled": "true"},
		"unsafe integer": {"mode": "test", "count": 9007199254740992.0},
		"nested":         {"mode": "test", "ratio": map[string]any{}}, "null": nil,
	} {
		t.Run(name, func(t *testing.T) {
			copy := plan.Clone()
			copy.BeforeBoot[0].Parameters = params
			if registry.Validate(copy) == nil {
				t.Fatal("invalid parameters accepted")
			}
		})
	}
	copy := plan.Clone()
	copy.OnSuccess = copy.BeforeBoot
	if registry.Validate(copy) == nil {
		t.Fatal("duplicate IDs accepted")
	}
	copy = plan.Clone()
	copy.Version = 2
	if registry.Validate(copy) == nil {
		t.Fatal("unknown version accepted")
	}
	copy = plan.Clone()
	copy.BeforeBoot[0].Task = "missing"
	if registry.Validate(copy) == nil {
		t.Fatal("unknown task accepted")
	}
	for _, definition := range []Definition{
		{Descriptor: Descriptor{ID: "example", TimeoutSeconds: 0}},
		{Descriptor: Descriptor{ID: "example", TimeoutSeconds: 1, Parameters: []Parameter{{Name: "bad", Type: "object"}}}, Run: func(context.Context, Context, map[string]any, func(string, string)) error { return nil }},
	} {
		if _, err := New(definition); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
}

func TestTimeoutIsJoinedAndErrorsSanitized(t *testing.T) {
	completed := false
	registry, err := New(Definition{Descriptor: Descriptor{ID: "example.wait", TimeoutSeconds: 0.01}, Run: func(ctx context.Context, _ Context, _ map[string]any, _ func(string, string)) error {
		<-ctx.Done()
		completed = true
		return errors.New("sensitive detail")
	}})
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	err = registry.Run(context.Background(), BeforeBoot, []Invocation{{ID: "wait", Task: "example.wait", Parameters: map[string]any{}}}, Context{}, func(string, string) {}, func(result Result) { results = append(results, result) })
	if !completed || !errors.Is(err, context.DeadlineExceeded) || len(results) != 2 || results[1].Error != "task timed out" {
		t.Fatalf("timeout result: %v %#v", err, results)
	}
	if results[1].FinishedUTC == nil || time.Since(*results[1].FinishedUTC) > time.Second {
		t.Fatal("missing finish time")
	}
}
