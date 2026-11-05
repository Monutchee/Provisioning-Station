// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

func submissionFixture(t *testing.T) (*Manager, *artifact.Store, Request) {
	t.Helper()
	store, err := artifact.OpenStore(t.TempDir(), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	runner := &controlledRunner{started: make(chan string, 32), release: make(chan struct{})}
	manager, err := OpenManager(t.TempDir(), store, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, store, Request{RequestID: strings.Repeat("a", 32), ArtifactID: stored.ID, HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10"}
}

func TestSubmissionConcurrentReplayAndConflict(t *testing.T) {
	manager, _, request := submissionFixture(t)
	var wait sync.WaitGroup
	results := make(chan Job, 24)
	for range 24 {
		wait.Go(func() {
			job, err := manager.Create(request)
			if err != nil {
				t.Error(err)
				return
			}
			results <- job
		})
	}
	wait.Wait()
	close(results)
	var id string
	for job := range results {
		if id == "" {
			id = job.ID
		}
		if job.ID != id {
			t.Fatal("duplicate accepted jobs")
		}
	}
	if len(manager.List()) != 1 {
		t.Fatal("duplicate jobs")
	}
	original, err := manager.Submission(request.RequestID)
	if err != nil || original.ID != id {
		t.Fatalf("reconcile: %+v %v", original, err)
	}
	request.TargetID = "2"
	_, err = manager.Create(request)
	var conflict *RequestConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestSubmissionReplayBeforeArtifactValidation(t *testing.T) {
	manager, _, request := submissionFixture(t)
	original, err := manager.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	// A later retry must not depend on the artifact store still being available.
	manager.Close()
	manager.artifacts = nil
	replay, seen, err := manager.CreateSubmissionContext(context.Background(), request)
	if err != nil || !seen || replay.ID != original.ID {
		t.Fatalf("replay: %+v %v %v", replay, seen, err)
	}

}

func TestSubmissionRestartNeverEnqueuesAcceptedJobs(t *testing.T) {
	manager, store, request := submissionFixture(t)
	manager.Close()
	// Represent a crash after durable acceptance but before enqueue.
	id := strings.Repeat("b", 32)
	job := &Job{ID: id, Request: request, State: StateQueued}
	if err := os.Mkdir(filepath.Join(manager.root, id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistLocked(job); err != nil {
		t.Fatal(err)
	}
	runner := &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}
	reopened, err := OpenManager(manager.root, store, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, seen, err := reopened.CreateSubmissionContext(context.Background(), request)
	if err != nil || !seen || replay.ID != id || replay.State != StateFailed {
		t.Fatalf("restart replay: %+v %v %v", replay, seen, err)
	}
	select {
	case <-runner.started:
		t.Fatal("accepted job executed again")
	default:
	}
}

func TestPublishedJobCorruptionFailsClosed(t *testing.T) {
	for _, data := range []string{"{", `{"id":"bad"}`, `{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {}`} {
		t.Run(data, func(t *testing.T) {
			manager, store, _ := submissionFixture(t)
			manager.Close()
			path := filepath.Join(manager.root, strings.Repeat("b", 32))
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "job.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenManager(manager.root, store, &controlledRunner{})
			if err == nil {
				reopened.Close()
				t.Fatal("corrupt record accepted")
			}
		})
	}
}

func TestPublicationFailurePreservesExistingRecord(t *testing.T) {
	manager, _, request := submissionFixture(t)
	manager.Close()
	id := strings.Repeat("b", 32)
	directory := filepath.Join(manager.root, id)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "job.json")
	// A directory cannot be atomically replaced by job metadata. Never remove it.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistLocked(&Job{ID: id, Request: request, State: StateQueued}); err == nil {
		t.Fatal("expected publication failure")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("old destination removed: %v", err)
	}
}

func TestSubmissionInvalidIDsAndDuplicatePublishedKeys(t *testing.T) {
	manager, store, request := submissionFixture(t)
	for _, key := range []string{"a", strings.Repeat("A", 32), strings.Repeat("a", 33)} {
		request.RequestID = key
		if _, err := manager.Create(request); err == nil {
			t.Fatalf("accepted %q", key)
		}
	}
	manager.Close()
	request.RequestID = strings.Repeat("a", 32)
	for _, id := range []string{strings.Repeat("b", 32), strings.Repeat("c", 32)} {
		directory := filepath.Join(manager.root, id)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(Job{ID: id, Request: request, State: StateSucceeded})
		if err := os.WriteFile(filepath.Join(directory, "job.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenManager(manager.root, store, &controlledRunner{})
	if err == nil {
		reopened.Close()
		t.Fatal("duplicate published request keys accepted")
	}
}
