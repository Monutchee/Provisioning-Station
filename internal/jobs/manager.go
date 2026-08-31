// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

type Runner interface {
	Validate(artifact.StoredArtifact, Request) error
	Run(context.Context, artifact.StoredArtifact, Request, func(level, message string)) error
}

type Manager struct {
	root      string
	artifacts *artifact.Store
	runner    Runner

	context context.Context
	cancel  context.CancelFunc
	queue   chan string
	wait    sync.WaitGroup

	mutex       sync.RWMutex
	jobs        map[string]*Job
	events      map[string][]Event
	subscribers map[string]map[chan Event]struct{}
	cancellers  map[string]context.CancelFunc
}

func OpenManager(root string, artifacts *artifact.Store, runner Runner) (*Manager, error) {
	if artifacts == nil || runner == nil {
		return nil, fmt.Errorf("job manager requires an artifact store and runner")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve job store: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create job store: %w", err)
	}
	managerContext, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		root:        root,
		artifacts:   artifacts,
		runner:      runner,
		context:     managerContext,
		cancel:      cancel,
		queue:       make(chan string, 128),
		jobs:        make(map[string]*Job),
		events:      make(map[string][]Event),
		subscribers: make(map[string]map[chan Event]struct{}),
		cancellers:  make(map[string]context.CancelFunc),
	}
	if err := manager.load(); err != nil {
		cancel()
		return nil, err
	}
	manager.wait.Add(1)
	go manager.worker()
	return manager, nil
}

func (manager *Manager) Close() {
	manager.cancel()
	manager.mutex.Lock()
	for _, cancel := range manager.cancellers {
		cancel()
	}
	manager.mutex.Unlock()
	manager.wait.Wait()
}

func (manager *Manager) Create(request Request) (Job, error) {
	return manager.CreateContext(context.Background(), request)
}

func (manager *Manager) CreateContext(ctx context.Context, request Request) (Job, error) {
	if request.ArtifactID == "" {
		return Job{}, fmt.Errorf("artifactId must not be empty")
	}
	stored, err := manager.artifacts.Load(request.ArtifactID)
	if err != nil {
		return Job{}, err
	}
	if err := manager.artifacts.Verify(ctx, stored); err != nil {
		return Job{}, err
	}
	if err := manager.runner.Validate(stored, request); err != nil {
		return Job{}, err
	}
	id, err := randomID()
	if err != nil {
		return Job{}, err
	}
	job := &Job{
		ID:         id,
		Request:    request,
		State:      StateQueued,
		CreatedUTC: time.Now().UTC(),
	}
	if err := os.Mkdir(filepath.Join(manager.root, id), 0o700); err != nil {
		return Job{}, fmt.Errorf("create job directory: %w", err)
	}

	manager.mutex.Lock()
	manager.jobs[id] = job
	manager.events[id] = nil
	manager.emitLocked(job, "info", "Job queued")
	if err := manager.persistLocked(job); err != nil {
		delete(manager.jobs, id)
		delete(manager.events, id)
		manager.mutex.Unlock()
		return Job{}, err
	}
	result := *job
	manager.mutex.Unlock()

	select {
	case manager.queue <- id:
		return result, nil
	case <-manager.context.Done():
		return Job{}, fmt.Errorf("station is shutting down")
	}
}

func (manager *Manager) Get(id string) (Job, error) {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	job, ok := manager.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("job %s was not found", id)
	}
	return *job, nil
}

func (manager *Manager) List() []Job {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	result := make([]Job, 0, len(manager.jobs))
	for _, job := range manager.jobs {
		result = append(result, *job)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedUTC.After(result[j].CreatedUTC) })
	return result
}

func (manager *Manager) Cancel(id string) (Job, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	job, ok := manager.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("job %s was not found", id)
	}
	if job.State.Terminal() {
		return *job, nil
	}
	if cancel := manager.cancellers[id]; cancel != nil {
		cancel()
	} else {
		now := time.Now().UTC()
		job.State = StateCanceled
		job.FinishedUTC = &now
		manager.emitLocked(job, "warning", "Job canceled before execution")
		_ = manager.persistLocked(job)
	}
	return *job, nil
}

func (manager *Manager) Events(id string, after int) ([]Event, error) {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	if _, ok := manager.jobs[id]; !ok {
		return nil, fmt.Errorf("job %s was not found", id)
	}
	all := manager.events[id]
	result := make([]Event, 0, len(all))
	for _, event := range all {
		if event.Sequence > after {
			result = append(result, event)
		}
	}
	return result, nil
}

func (manager *Manager) Subscribe(id string, after int) ([]Event, <-chan Event, func(), error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	job, ok := manager.jobs[id]
	if !ok {
		return nil, nil, nil, fmt.Errorf("job %s was not found", id)
	}
	history := make([]Event, 0)
	for _, event := range manager.events[id] {
		if event.Sequence > after {
			history = append(history, event)
		}
	}
	channel := make(chan Event, 256)
	if !job.State.Terminal() {
		if manager.subscribers[id] == nil {
			manager.subscribers[id] = make(map[chan Event]struct{})
		}
		manager.subscribers[id][channel] = struct{}{}
	} else {
		close(channel)
	}
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			manager.mutex.Lock()
			defer manager.mutex.Unlock()
			if subscribers := manager.subscribers[id]; subscribers != nil {
				if _, exists := subscribers[channel]; exists {
					delete(subscribers, channel)
					close(channel)
				}
			}
		})
	}
	return history, channel, unsubscribe, nil
}

func (manager *Manager) worker() {
	defer manager.wait.Done()
	for {
		select {
		case <-manager.context.Done():
			return
		case id := <-manager.queue:
			manager.run(id)
		}
	}
}

func (manager *Manager) run(id string) {
	manager.mutex.Lock()
	job := manager.jobs[id]
	if job == nil || job.State != StateQueued {
		manager.mutex.Unlock()
		return
	}
	runContext, cancel := context.WithCancel(manager.context)
	manager.cancellers[id] = cancel
	now := time.Now().UTC()
	job.State = StateRunning
	job.StartedUTC = &now
	manager.emitLocked(job, "info", "Job started")
	_ = manager.persistLocked(job)
	request := job.Request
	manager.mutex.Unlock()

	stored, loadErr := manager.artifacts.Load(request.ArtifactID)
	var runErr error
	if loadErr != nil {
		runErr = loadErr
	} else if verifyErr := manager.artifacts.Verify(runContext, stored); verifyErr != nil {
		runErr = verifyErr
	} else {
		runErr = manager.runner.Run(runContext, stored, request, func(level, message string) {
			manager.emit(id, level, message)
		})
	}
	cancel()

	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	delete(manager.cancellers, id)
	job = manager.jobs[id]
	if job == nil {
		return
	}
	finished := time.Now().UTC()
	job.FinishedUTC = &finished
	switch {
	case errors.Is(runErr, context.Canceled):
		job.State = StateCanceled
		job.Error = "canceled"
		manager.emitLocked(job, "warning", "Job canceled")
	case runErr != nil:
		job.State = StateFailed
		job.Error = runErr.Error()
		manager.emitLocked(job, "error", "Job failed: "+runErr.Error())
	default:
		job.State = StateSucceeded
		manager.emitLocked(job, "info", "Job completed successfully")
	}
	_ = manager.persistLocked(job)
	manager.closeSubscribersLocked(id)
}

func (manager *Manager) emit(id, level, message string) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	job := manager.jobs[id]
	if job != nil {
		manager.emitLocked(job, level, message)
	}
}

func (manager *Manager) emitLocked(job *Job, level, message string) {
	event := Event{
		Sequence: len(manager.events[job.ID]) + 1,
		Time:     time.Now().UTC(),
		Level:    level,
		Message:  message,
	}
	manager.events[job.ID] = append(manager.events[job.ID], event)
	job.EventCount = event.Sequence
	_ = manager.appendEventLocked(job.ID, event)
	for channel := range manager.subscribers[job.ID] {
		select {
		case channel <- event:
		default:
		}
	}
}

func (manager *Manager) closeSubscribersLocked(id string) {
	for channel := range manager.subscribers[id] {
		close(channel)
	}
	delete(manager.subscribers, id)
}

func (manager *Manager) load() error {
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		return fmt.Errorf("read job store: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		data, err := os.ReadFile(filepath.Join(manager.root, id, "job.json"))
		if err != nil {
			continue
		}
		var job Job
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&job); err != nil || job.ID != id {
			continue
		}
		manager.jobs[id] = &job
		manager.events[id] = manager.loadEvents(id)
		job.EventCount = len(manager.events[id])
		if job.State == StateQueued || job.State == StateRunning {
			now := time.Now().UTC()
			job.State = StateFailed
			job.FinishedUTC = &now
			job.Error = "station stopped before the job completed"
			manager.emitLocked(&job, "error", job.Error)
			_ = manager.persistLocked(&job)
		}
	}
	return nil
}

func (manager *Manager) loadEvents(id string) []Event {
	file, err := os.Open(filepath.Join(manager.root, id, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	result := make([]Event, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			result = append(result, event)
		}
	}
	return result
}

func (manager *Manager) persistLocked(job *Job) error {
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(manager.root, job.ID, "job.json")
	temporary, err := os.CreateTemp(filepath.Dir(path), ".job-*.json")
	if err != nil {
		return fmt.Errorf("create job metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write job metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close job metadata: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return fmt.Errorf("publish job metadata: %w", retryErr)
		}
	}
	return nil
}

func (manager *Manager) appendEventLocked(id string, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(manager.root, id, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate job ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}
