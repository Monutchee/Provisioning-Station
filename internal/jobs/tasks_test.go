// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/tasks"
)

type taskRunner struct{ run func(context.Context) error }

func (runner taskRunner) Validate(artifact.StoredArtifact, Request) error { return nil }
func (runner taskRunner) Run(ctx context.Context, _ artifact.StoredArtifact, _ Request, _ func(string, string)) error {
	if err := tasks.ExecuteBeforeBoot(ctx); err != nil {
		return err
	}
	return runner.run(ctx)
}

func TestDeploymentTaskOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		bootErr    error
		preErr     bool
		successErr bool
		phase      string
		state      State
	}{
		{name: "success", phase: tasks.OnSuccess, state: StateSucceeded},
		{name: "boot failure", bootErr: errors.New("synthetic boot failure"), phase: tasks.OnFailure, state: StateFailed},
		{name: "prepare failure", preErr: true, phase: tasks.OnFailure, state: StateFailed},
		{name: "success hook failure", successErr: true, phase: tasks.OnSuccess, state: StateFailed},
		{name: "boot canceled", bootErr: context.Canceled, phase: tasks.OnCancel, state: StateCanceled},
		{name: "boot deadline", bootErr: context.DeadlineExceeded, phase: tasks.OnFailure, state: StateFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, err := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
			if err != nil {
				t.Fatal(err)
			}
			var mutex sync.Mutex
			var order []string
			record := func(value string) { mutex.Lock(); defer mutex.Unlock(); order = append(order, value) }
			var definitions []tasks.Definition
			plan := &tasks.Plan{Version: 1}
			for _, phase := range []string{tasks.BeforeBoot, tasks.OnSuccess, tasks.OnFailure, tasks.OnCancel} {
				phase := phase
				definitions = append(definitions, tasks.Definition{Descriptor: tasks.Descriptor{ID: phase, TimeoutSeconds: 1}, Run: func(ctx context.Context, _ tasks.Context, _ map[string]any, _ func(string, string)) error {
					if ctx.Err() != nil {
						t.Error("hook inherited canceled context")
					}
					record(phase)
					if phase == tasks.BeforeBoot && scenario.preErr || phase == tasks.OnSuccess && scenario.successErr {
						return errors.New("synthetic task failure")
					}
					return nil
				}})
				invocation := []tasks.Invocation{{ID: phase, Task: phase, Parameters: map[string]any{}}}
				switch phase {
				case tasks.BeforeBoot:
					plan.BeforeBoot = invocation
				case tasks.OnSuccess:
					plan.OnSuccess = invocation
				case tasks.OnFailure:
					plan.OnFailure = invocation
				case tasks.OnCancel:
					plan.OnCancel = invocation
				}
			}
			registry, err := tasks.New(definitions...)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := OpenManager(filepath.Join(t.TempDir(), "jobs"), store, taskRunner{run: func(context.Context) error { record("boot"); return scenario.bootErr }}, WithDeploymentTasks(registry))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			job, err := manager.Create(Request{ArtifactID: stored.ID, DeploymentTasks: plan})
			if err != nil {
				t.Fatal(err)
			}
			waitForState(t, manager, job.ID, scenario.state)
			want := []string{tasks.BeforeBoot}
			if !scenario.preErr {
				want = append(want, "boot")
			}
			want = append(want, scenario.phase)
			mutex.Lock()
			if !reflect.DeepEqual(order, want) {
				t.Errorf("order=%v want=%v", order, want)
			}
			mutex.Unlock()
			job, _ = manager.Get(job.ID)
			if len(job.TaskResults) != 2 {
				t.Fatalf("results=%+v", job.TaskResults)
			}
			if scenario.successErr && job.DeploymentOutcome != "succeeded" {
				t.Fatal("boot outcome lost")
			}
		})
	}
}

func TestCancellationDuringOutcomeDoesNotSelectAnotherPhase(t *testing.T) {
	for _, phase := range []string{tasks.OnSuccess, tasks.OnFailure} {
		t.Run(phase, func(t *testing.T) {
			store, _ := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
			stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			finished := make(chan struct{})
			var second bool
			registry, _ := tasks.New(tasks.Definition{Descriptor: tasks.Descriptor{ID: "wait", TimeoutSeconds: 1}, Run: func(ctx context.Context, _ tasks.Context, _ map[string]any, _ func(string, string)) error {
				close(started)
				<-ctx.Done()
				close(finished)
				return ctx.Err()
			}}, tasks.Definition{Descriptor: tasks.Descriptor{ID: "second", TimeoutSeconds: 1}, Run: func(context.Context, tasks.Context, map[string]any, func(string, string)) error {
				second = true
				return nil
			}})
			invocations := []tasks.Invocation{{ID: "wait", Task: "wait", Parameters: map[string]any{}}, {ID: "second", Task: "second", Parameters: map[string]any{}}}
			plan := &tasks.Plan{Version: 1, OnCancel: []tasks.Invocation{{ID: "cancel", Task: "second", Parameters: map[string]any{}}}}
			if phase == tasks.OnSuccess {
				plan.OnSuccess = invocations
			} else {
				plan.OnFailure = invocations
			}
			manager, err := OpenManager(t.TempDir(), store, taskRunner{run: func(context.Context) error {
				if phase == tasks.OnFailure {
					return errors.New("boot failed")
				}
				return nil
			}}, WithDeploymentTasks(registry))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			job, err := manager.Create(Request{ArtifactID: stored.ID, DeploymentTasks: plan})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("hook did not start")
			}
			if _, err := manager.Cancel(job.ID); err != nil {
				t.Fatal(err)
			}
			waitForState(t, manager, job.ID, StateCanceled)
			if phase == tasks.OnFailure {
				final, _ := manager.Get(job.ID)
				if final.DeploymentError != "boot failed" || final.DeploymentOutcome != "failed" {
					t.Fatalf("primary failure lost: %+v", final)
				}
			}
			<-finished
			if second {
				t.Fatal("another task or outcome phase launched after cancellation")
			}
		})
	}
}

func TestUnknownTasksRejectedBeforeExecution(t *testing.T) {
	store, _ := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	manager, err := OpenManager(t.TempDir(), store, taskRunner{run: func(context.Context) error { t.Fatal("runner executed"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	_, err = manager.Create(Request{ArtifactID: "not-needed", DeploymentTasks: &tasks.Plan{Version: 1, BeforeBoot: []tasks.Invocation{{ID: "unknown", Task: "unknown", Parameters: map[string]any{}}}}})
	if err == nil {
		t.Fatal("unknown task accepted")
	}
	if len(manager.List()) != 0 {
		t.Fatal("invalid job persisted")
	}
}

func TestCancelHooksHaveOneFiveSecondBudget(t *testing.T) {
	store, _ := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan time.Time, 1)
	registry, _ := tasks.New(tasks.Definition{Descriptor: tasks.Descriptor{ID: "cleanup", TimeoutSeconds: 30}, Run: func(ctx context.Context, _ tasks.Context, _ map[string]any, _ func(string, string)) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > tasks.CancelTimeout {
			t.Error("cleanup context is not bounded by 5 seconds")
		}
		started <- time.Now()
		<-ctx.Done()
		return ctx.Err()
	}})
	manager, err := OpenManager(t.TempDir(), store, taskRunner{run: func(context.Context) error { return context.Canceled }}, WithDeploymentTasks(registry))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	plan := &tasks.Plan{Version: 1, OnCancel: []tasks.Invocation{{ID: "cleanup", Task: "cleanup", Parameters: map[string]any{}}}}
	job, err := manager.Create(Request{ArtifactID: stored.ID, DeploymentTasks: plan})
	if err != nil {
		t.Fatal(err)
	}
	start := <-started
	for time.Since(start) < 7*time.Second {
		job, _ = manager.Get(job.ID)
		if job.State.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.State != StateCanceled || len(job.TaskErrors) != 1 || time.Since(start) < 4*time.Second {
		t.Fatalf("cleanup budget result=%+v", job)
	}
}

func TestTaskSnapshotsSerializationAndRestart(t *testing.T) {
	store, _ := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	started := make(chan string, 2)
	release := make(chan struct{})
	registry, _ := tasks.New(tasks.Definition{Descriptor: tasks.Descriptor{ID: "capture", TimeoutSeconds: 1, Parameters: []tasks.Parameter{{Name: "value", Type: "string", Required: true}}}, Run: func(ctx context.Context, _ tasks.Context, params map[string]any, _ func(string, string)) error {
		started <- params["value"].(string)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	manager, err := OpenManager(root, store, taskRunner{run: func(context.Context) error { return nil }}, WithDeploymentTasks(registry))
	if err != nil {
		t.Fatal(err)
	}
	plan := &tasks.Plan{Version: 1, OnSuccess: []tasks.Invocation{{ID: "capture", Task: "capture", Parameters: map[string]any{"value": "original"}}}}
	first, err := manager.Create(Request{ArtifactID: stored.ID, DeploymentTasks: plan})
	if err != nil {
		t.Fatal(err)
	}
	if <-started != "original" {
		t.Fatal("wrong parameter")
	}
	plan.OnSuccess[0].Parameters["value"] = "changed"
	snapshot, _ := manager.Get(first.ID)
	snapshot.Request.DeploymentTasks.OnSuccess[0].Parameters["value"] = "external"
	second, err := manager.Create(Request{ArtifactID: stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondState, _ := manager.Get(second.ID)
	if secondState.State != StateQueued {
		t.Fatal("second job bypassed running hook")
	}
	close(release)
	waitForState(t, manager, first.ID, StateSucceeded)
	waitForState(t, manager, second.ID, StateSucceeded)
	snapshot, _ = manager.Get(first.ID)
	if snapshot.Request.DeploymentTasks.OnSuccess[0].Parameters["value"] != "original" {
		t.Fatal("request snapshot mutated")
	}
	manager.Close()
	// Model a persisted interruption without replaying any task on load.
	manager.mutex.Lock()
	manager.jobs[first.ID].State = StateRunning
	manager.jobs[first.ID].TaskResults[0].State = "running"
	_ = manager.persistLocked(manager.jobs[first.ID])
	manager.mutex.Unlock()
	reopened, err := OpenManager(root, store, taskRunner{run: func(context.Context) error { t.Error("replayed runner"); return nil }}, WithDeploymentTasks(registry))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, _ := reopened.Get(first.ID)
	if recovered.State != StateFailed || recovered.TaskResults[0].State != "interrupted" {
		t.Fatalf("recovered=%+v", recovered)
	}
	select {
	case <-started:
		t.Fatal("replayed task")
	default:
	}
}
