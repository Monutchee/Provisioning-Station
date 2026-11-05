// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
)

func retentionFixture(t *testing.T) (*Manager, *artifact.Store, *storagebudget.Gate, artifact.StoredArtifact) {
	t.Helper()
	root := t.TempDir()
	store, err := artifact.OpenStore(filepath.Join(root, "store"), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Import(context.Background(), bytes.NewReader(testArtifact(t)))
	if err != nil {
		t.Fatal(err)
	}
	gate := storagebudget.New(root, 32<<20)
	store.SetBudget(gate)
	m, err := OpenManager(filepath.Join(root, "jobs"), store, &controlledRunner{started: make(chan string, 256), release: make(chan struct{})}, WithStorageBudget(gate))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, store, gate, a
}
func seedCompleted(t *testing.T, m *Manager, a string, index int) *Job {
	t.Helper()
	j := &Job{ID: fmt.Sprintf("%032x", index+1), Request: Request{RequestID: fmt.Sprintf("%032x", index+1000), ArtifactID: a}, State: StateSucceeded, CreatedUTC: time.Unix(int64(index), 0)}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if err := os.Mkdir(filepath.Join(m.root, j.ID), 0700); err != nil {
		t.Fatal(err)
	}
	m.jobs[j.ID] = j
	m.submissions[j.Request.RequestID] = j.ID
	if err := m.persistLocked(j); err != nil {
		t.Fatal(err)
	}
	return j
}
func TestRetentionPreviewPruneAndDurableSubmissionExpiry(t *testing.T) {
	m, store, gate, a := retentionFixture(t)
	var oldest *Job
	for i := range 31 {
		j := seedCompleted(t, m, a.ID, i)
		if i == 0 {
			oldest = j
		}
	}
	unknown := filepath.Join(m.root, oldest.ID, "operator-note.txt")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := m.Cleanup(false)
	if err != nil || len(preview.JobIDs) != 1 || preview.JobIDs[0] != oldest.ID {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if len(m.List()) != 31 {
		t.Fatal("preview changed jobs")
	}
	applied, err := m.Cleanup(true)
	if err != nil || len(applied.JobIDs) != 1 || len(m.List()) != 30 {
		t.Fatalf("apply=%+v err=%v", applied, err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown file removed: %v", err)
	}
	_, _, err = m.CreateSubmissionContext(context.Background(), oldest.Request)
	var expired *ExpiredSubmissionError
	if !errors.As(err, &expired) {
		t.Fatalf("replay=%v", err)
	}
	changed := oldest.Request
	changed.TargetID = "different"
	_, err = m.Create(changed)
	var conflict *RequestConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict=%v", err)
	}
	m.Close()
	restarted, err := OpenManager(m.root, store, &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}, WithStorageBudget(gate))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	_, err = restarted.Submission(oldest.Request.RequestID)
	if !errors.As(err, &expired) {
		t.Fatalf("restart=%v", err)
	}
	if _, err := store.Load(a.ID); err != nil {
		t.Fatalf("prune evicted firmware unnecessarily: %v", err)
	}
}
func TestPruneJournalCrashBeforeRemovingJob(t *testing.T) {
	m, store, gate, a := retentionFixture(t)
	j := seedCompleted(t, m, a.ID, 0)
	data, _ := json.Marshal(tombstone{j.ID, fingerprint(j.Request)})
	if err := m.writeTombstone(j.Request.RequestID, data); err != nil {
		t.Fatal(err)
	}
	// Crash window: durable prune intent exists alongside the old job directory.
	m.Close()
	restarted, err := OpenManager(m.root, store, &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}, WithStorageBudget(gate))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if len(restarted.List()) != 0 {
		t.Fatal("journaled job reappeared")
	}
	_, _, err = restarted.CreateSubmissionContext(context.Background(), j.Request)
	var expired *ExpiredSubmissionError
	if !errors.As(err, &expired) {
		t.Fatalf("journal replay=%v", err)
	}
}
func TestStorageBudgetProtectsActiveArtifactAndReservesOutput(t *testing.T) {
	m, store, g, a := retentionFixture(t)
	g.Lock()
	s, err := g.StatusLocked()
	if err != nil {
		t.Fatal(err)
	}
	g.Limit = s.UsedBytes + s.ReservedBytes + m.outputReservation() + 8192
	g.Unlock()
	job, err := m.Create(Request{ArtifactID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Upload cannot borrow output/completion reservations or evict active firmware.
	huge := bytes.Repeat([]byte{'x'}, 32768)
	_, err = store.Import(context.Background(), bytes.NewReader(huge))
	if !errors.Is(err, storagebudget.ErrFull) {
		t.Fatalf("upload=%v", err)
	}
	if _, err := store.Load(a.ID); err != nil {
		t.Fatalf("active artifact removed: %v", err)
	}
	s, err = m.StorageStatus()
	if err != nil || s.UsedBytes+s.ReservedBytes > s.LimitBytes {
		t.Fatalf("budget=%+v %v", s, err)
	}
	if _, err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
}
func TestArtifactEvictionOrderAndUnknownStateProtection(t *testing.T) {
	m, store, g, a := retentionFixture(t)
	seedCompleted(t, m, a.ID, 0)
	raw := testArtifact(t)
	binary.LittleEndian.PutUint32(raw[4:8], 1234)
	b, err := store.Import(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// a is referenced; b is unreferenced and must be selected first.
	g.Lock()
	s, _ := g.StatusLocked()
	g.Limit = s.UsedBytes + s.ReservedBytes - 1
	g.Unlock()
	preview, err := m.Cleanup(false)
	if err != nil || len(preview.ArtifactIDs) != 1 || preview.ArtifactIDs[0] != b.ID {
		t.Fatalf("priority=%+v %v", preview, err)
	}
	if _, err := m.Cleanup(true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Directory(a.ID), "unknown"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	g.Lock()
	s, _ = g.StatusLocked()
	g.Limit = s.UsedBytes + s.ReservedBytes - 1
	g.Unlock()
	result, err := m.Cleanup(true)
	if err != nil || len(result.ArtifactIDs) != 0 {
		t.Fatalf("unknown=%+v %v", result, err)
	}
	if _, err := store.Load(a.ID); err != nil {
		t.Fatal("unknown-containing artifact deleted")
	}
}
func TestConcurrentAdmissionRejectsFullQueueWithoutBlockingCompletion(t *testing.T) {
	m, _, _, a := retentionFixture(t)
	first, err := m.Create(Request{ArtifactID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.runner.(*controlledRunner).started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	// Saturate the queue without creating 128 metadata directories. Invalid IDs
	// are harmless worker wakeups; the active worker remains blocked in runner.
	for range cap(m.queue) {
		m.queue <- "missing"
	}
	done := make(chan error, 1)
	go func() { _, err := m.Create(Request{ArtifactID: a.ID}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "queue is full") {
			t.Fatalf("admission=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admission blocked under budget gate")
	}
	_, err = m.Cancel(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, m, first.ID, StateCanceled)
}
func TestBoundedEventsRetainTerminalMetadataAndRestart(t *testing.T) {
	m, _, _, a := retentionFixture(t)
	j := seedCompleted(t, m, a.ID, 0)
	m.mutex.Lock()
	j.State = StateRunning
	for range 150 {
		m.emitLocked(j, "info", strings.Repeat("x", 16384))
	}
	j.State = StateFailed
	j.Error = "terminal failure"
	m.emitLocked(j, "error", j.Error)
	err := m.persistLocked(j)
	m.mutex.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(m.root, j.ID, "events.jsonl"))
	if err != nil || info.Size() > eventLimitBytes+(32<<10) {
		t.Fatalf("events=%v %v", info, err)
	}
	got, err := m.Get(j.ID)
	if err != nil || !got.EventsTruncated || got.Error != "terminal failure" {
		t.Fatalf("terminal=%+v %v", got, err)
	}
	events, err := m.Events(j.ID, 0)
	if err != nil || events[len(events)-1].Message != "terminal failure" {
		t.Fatalf("terminal event missing %v", err)
	}
}
func TestConcurrentImportsStayWithinBudget(t *testing.T) {
	m, store, g, _ := retentionFixture(t)
	g.Lock()
	s, _ := g.StatusLocked()
	g.Limit = s.UsedBytes + s.ReservedBytes + 3000
	g.Unlock()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := testArtifact(t)
			binary.LittleEndian.PutUint32(data[4:8], uint32(i+1))
			_, _ = store.Import(context.Background(), bytes.NewReader(data))
		}(i)
	}
	wg.Wait()
	status, err := m.StorageStatus()
	if err != nil || status.UsedBytes+status.ReservedBytes > status.LimitBytes {
		t.Fatalf("concurrent=%+v %v", status, err)
	}
}

func TestPruningPersistenceFailurePreservesPublishedJob(t *testing.T) {
	m, _, _, a := retentionFixture(t)
	var oldest *Job
	for i := range 31 {
		j := seedCompleted(t, m, a.ID, i)
		if i == 0 {
			oldest = j
		}
	}
	// A non-directory prevents publishing durable prune intent. No accepted job
	// may disappear from either reconciliation or disk on that failure.
	if err := os.WriteFile(filepath.Join(m.root, "submissions"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cleanup(true); err == nil {
		t.Fatal("cleanup ignored persistence failure")
	}
	if _, err := m.Submission(oldest.Request.RequestID); err != nil {
		t.Fatalf("accepted record lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, oldest.ID, "job.json")); err != nil {
		t.Fatal(err)
	}
}
func TestCorruptPruneJournalFailsClosed(t *testing.T) {
	m, store, g, a := retentionFixture(t)
	j := seedCompleted(t, m, a.ID, 0)
	m.Close()
	dir := filepath.Join(m.root, "submissions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, j.Request.RequestID+".json"), []byte(`{"jobId":"bad","fingerprint":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenManager(m.root, store, &controlledRunner{started: make(chan string, 1), release: make(chan struct{})}, WithStorageBudget(g))
	if err == nil {
		restarted.Close()
		t.Fatal("corrupt submission history accepted")
	}
}
