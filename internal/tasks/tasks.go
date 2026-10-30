// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package tasks executes trusted, compiled deployment tasks. Requests select
// identifiers and flat parameters, never executables or shell commands.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"
)

const (
	BeforeBoot    = "beforeBoot"
	OnSuccess     = "onSuccess"
	OnFailure     = "onFailure"
	OnCancel      = "onCancel"
	CancelTimeout = 5 * time.Second
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type Invocation struct {
	ID         string         `json:"id"`
	Task       string         `json:"task"`
	Parameters map[string]any `json:"parameters"`
}

type Plan struct {
	Version    int          `json:"version"`
	BeforeBoot []Invocation `json:"beforeBoot"`
	OnSuccess  []Invocation `json:"onSuccess"`
	OnFailure  []Invocation `json:"onFailure"`
	OnCancel   []Invocation `json:"onCancel"`
}

func (plan *Plan) Phase(phase string) []Invocation {
	if plan == nil {
		return nil
	}
	switch phase {
	case BeforeBoot:
		return plan.BeforeBoot
	case OnSuccess:
		return plan.OnSuccess
	case OnFailure:
		return plan.OnFailure
	case OnCancel:
		return plan.OnCancel
	}
	return nil
}

// Clone also freezes parameter maps and numeric representations.
func (plan *Plan) Clone() *Plan {
	if plan == nil {
		return nil
	}
	copy := &Plan{Version: plan.Version}
	clone := func(source []Invocation) []Invocation {
		if source == nil {
			return []Invocation{}
		}
		result := make([]Invocation, len(source))
		for i, item := range source {
			result[i] = item
			if item.Parameters != nil {
				result[i].Parameters = make(map[string]any, len(item.Parameters))
				for key, value := range item.Parameters {
					result[i].Parameters[key] = value
				}
			}
		}
		return result
	}
	copy.BeforeBoot, copy.OnSuccess, copy.OnFailure, copy.OnCancel = clone(plan.BeforeBoot), clone(plan.OnSuccess), clone(plan.OnFailure), clone(plan.OnCancel)
	return copy
}

type Parameter struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Enum     []any  `json:"enum,omitempty"`
}

type Descriptor struct {
	ID             string      `json:"id"`
	Parameters     []Parameter `json:"parameters"`
	TimeoutSeconds float64     `json:"timeoutSeconds"`
}

// Context contains only explicit deployment identity; implementations must honor
// cancellation and must not log sensitive parameter values or device credentials.
type Context struct {
	JobID             string
	ArtifactID        string
	HWServerURL       string
	BoardIP           string
	TargetID          string
	TargetCableSerial string
	TargetDeviceIndex string
}

type Definition struct {
	Descriptor Descriptor
	Validate   func(map[string]any) error
	Run        func(context.Context, Context, map[string]any, func(string, string)) error
}

type Registry struct{ definitions map[string]Definition }

func New(definitions ...Definition) (*Registry, error) {
	registry := &Registry{definitions: make(map[string]Definition)}
	for _, definition := range definitions {
		descriptor := definition.Descriptor
		if !identifier.MatchString(descriptor.ID) || definition.Run == nil || math.IsNaN(descriptor.TimeoutSeconds) || math.IsInf(descriptor.TimeoutSeconds, 0) || descriptor.TimeoutSeconds <= 0 || descriptor.TimeoutSeconds > 3600 || time.Duration(descriptor.TimeoutSeconds*float64(time.Second)) <= 0 {
			return nil, fmt.Errorf("invalid task descriptor %q", descriptor.ID)
		}
		if _, exists := registry.definitions[descriptor.ID]; exists {
			return nil, fmt.Errorf("duplicate task %q", descriptor.ID)
		}
		descriptor.Parameters = append([]Parameter{}, descriptor.Parameters...)
		names := map[string]bool{}
		for i, parameter := range descriptor.Parameters {
			if !identifier.MatchString(parameter.Name) || names[parameter.Name] || !validType(parameter.Type) {
				return nil, fmt.Errorf("invalid parameter descriptor for task %q", descriptor.ID)
			}
			names[parameter.Name] = true
			for _, value := range parameter.Enum {
				if !matches(parameter.Type, value) {
					return nil, fmt.Errorf("invalid enum for task %q", descriptor.ID)
				}
			}
			descriptor.Parameters[i].Enum = append([]any(nil), parameter.Enum...)
		}
		definition.Descriptor = descriptor
		registry.definitions[descriptor.ID] = definition
	}
	return registry, nil
}

func validType(kind string) bool {
	return kind == "string" || kind == "boolean" || kind == "integer" || kind == "number"
}
func number(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	}
	return 0, false
}
func matches(kind string, value any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number", "integer":
		n, ok := number(value)
		return ok && (kind == "number" || math.Trunc(n) == n && math.Abs(n) <= 9007199254740991)
	}
	return false
}
func same(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x == y
	}
	return a == b
}

func (registry *Registry) Validate(plan *Plan) error {
	if plan == nil {
		return nil
	}
	if plan.Version != 1 {
		return fmt.Errorf("unsupported deploymentTasks version")
	}
	ids := map[string]bool{}
	count := 0
	for _, phase := range []string{BeforeBoot, OnSuccess, OnFailure, OnCancel} {
		for _, invocation := range plan.Phase(phase) {
			count++
			if count > 64 || !identifier.MatchString(invocation.ID) || ids[invocation.ID] {
				return fmt.Errorf("deployment tasks require at most 64 unique valid invocation IDs")
			}
			ids[invocation.ID] = true
			if err := registry.validateInvocation(invocation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (registry *Registry) validateInvocation(invocation Invocation) error {
	var definition Definition
	var exists bool
	if registry != nil {
		definition, exists = registry.definitions[invocation.Task]
	}
	if !exists {
		return fmt.Errorf("unknown deployment task %q", invocation.Task)
	}
	if invocation.Parameters == nil {
		return fmt.Errorf("task %q requires a parameters object", invocation.ID)
	}
	known := map[string]bool{}
	for _, parameter := range definition.Descriptor.Parameters {
		known[parameter.Name] = true
		value, present := invocation.Parameters[parameter.Name]
		if !present {
			if parameter.Required {
				return fmt.Errorf("task %q requires parameter %q", invocation.ID, parameter.Name)
			}
			continue
		}
		if !matches(parameter.Type, value) {
			return fmt.Errorf("task %q has invalid parameter %q", invocation.ID, parameter.Name)
		}
		if len(parameter.Enum) > 0 {
			allowed := false
			for _, option := range parameter.Enum {
				if same(value, option) {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("task %q has invalid enum parameter %q", invocation.ID, parameter.Name)
			}
		}
	}
	for key := range invocation.Parameters {
		if !known[key] {
			return fmt.Errorf("task %q has unknown parameter %q", invocation.ID, key)
		}
	}
	if definition.Validate != nil {
		parameters := make(map[string]any, len(invocation.Parameters))
		for key, value := range invocation.Parameters {
			parameters[key] = value
		}
		if err := definition.Validate(parameters); err != nil {
			return fmt.Errorf("task %q semantic parameter validation failed", invocation.ID)
		}
	}
	return nil
}

type Capabilities struct {
	Versions []int        `json:"versions"`
	Phases   []string     `json:"phases"`
	Tasks    []Descriptor `json:"tasks"`
}

func (registry *Registry) Capabilities() Capabilities {
	result := Capabilities{Versions: []int{1}, Phases: []string{BeforeBoot, OnSuccess, OnFailure, OnCancel}, Tasks: []Descriptor{}}
	if registry != nil {
		for _, definition := range registry.definitions {
			descriptor := definition.Descriptor
			descriptor.Parameters = append([]Parameter{}, descriptor.Parameters...)
			for i := range descriptor.Parameters {
				descriptor.Parameters[i].Enum = append([]any(nil), descriptor.Parameters[i].Enum...)
			}
			result.Tasks = append(result.Tasks, descriptor)
		}
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	return result
}

type Result struct {
	ID          string     `json:"id"`
	Task        string     `json:"task"`
	Phase       string     `json:"phase"`
	State       string     `json:"state"`
	StartedUTC  time.Time  `json:"startedUtc"`
	FinishedUTC *time.Time `json:"finishedUtc,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// Run is sequential and fail-fast. Implementations are trusted code and must
// return when ctx is done; arbitrary goroutines cannot safely be force-stopped.
func (registry *Registry) Run(ctx context.Context, phase string, invocations []Invocation, deployment Context, emit func(string, string), observe func(Result)) error {
	for _, invocation := range invocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := registry.validateInvocation(invocation); err != nil {
			return err
		}
		definition := registry.definitions[invocation.Task]
		taskContext, cancel := context.WithTimeout(ctx, time.Duration(definition.Descriptor.TimeoutSeconds*float64(time.Second)))
		result := Result{ID: invocation.ID, Task: invocation.Task, Phase: phase, State: "running", StartedUTC: time.Now().UTC()}
		observe(result)
		parameters := map[string]any{}
		for key, value := range invocation.Parameters {
			parameters[key] = value
		}
		err := definition.Run(taskContext, deployment, parameters, emit)
		if taskContext.Err() != nil {
			err = taskContext.Err()
		}
		cancel()
		now := time.Now().UTC()
		result.FinishedUTC = &now
		result.State = "succeeded"
		if err != nil {
			result.State = "failed"
			result.Error = "task execution failed"
			if errors.Is(err, context.Canceled) {
				result.State = "canceled"
				result.Error = "task canceled"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				result.Error = "task timed out"
			}
		}
		observe(result)
		if err != nil {
			return fmt.Errorf("deployment task %q: %s: %w", invocation.ID, result.Error, safeCause(err))
		}
	}
	return nil
}
func safeCause(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("execution failed")
}

type beforeBootKey struct{}

func WithBeforeBoot(ctx context.Context, callback func(context.Context) error) context.Context {
	return context.WithValue(ctx, beforeBootKey{}, callback)
}
func ExecuteBeforeBoot(ctx context.Context) error {
	if callback, ok := ctx.Value(beforeBootKey{}).(func(context.Context) error); ok {
		return callback(ctx)
	}
	return ctx.Err()
}
