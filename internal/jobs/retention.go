// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
)

const completedJobLimit = 30
const eventLimitBytes = 1 << 20
const metadataReserve = 4 << 20
const maintenanceReserve = 4 << 20

type ExpiredSubmissionError struct{ JobID string }

func (e *ExpiredSubmissionError) Error() string {
	return "submission history expired; accepted job " + e.JobID + " will never be executed again"
}

type tombstone struct {
	JobID       string `json:"jobId"`
	Fingerprint string `json:"fingerprint"`
}

func fingerprint(r Request) string {
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func WithStorageBudget(g *storagebudget.Gate) ManagerOption {
	return func(m *Manager) error {
		if g == nil {
			return errors.New("nil storage budget")
		}
		m.budget = g
		return nil
	}
}

type CleanupResult struct {
	storagebudget.Status
	Apply            bool     `json:"apply"`
	JobIDs           []string `json:"jobIds"`
	ArtifactIDs      []string `json:"artifactIds"`
	ReclaimableBytes int64    `json:"reclaimableBytes"`
}

func (m *Manager) outputReservation() int64 {
	n := int64(eventLimitBytes + metadataReserve)
	if m.console != nil {
		n += m.maxConsoleLogBytes
	}
	return n
}
func (m *Manager) initializeRetention() error {
	if m.budget == nil {
		return nil
	}
	m.budget.Lock()
	defer m.budget.Unlock()
	m.budget.Reservations["maintenance"] = maintenanceReserve
	m.budget.Reclaim = func(need int64) error { _, err := m.cleanupLocked(true, need); return err }
	_, err := m.cleanupLocked(true, 0)
	if err == nil {
		if status, e := m.budget.StatusLocked(); e != nil {
			return e
		} else if status.OverBudget {
			log.Printf("storage budget exceeded by protected or unknown state; growth is disabled until space is available")
		}
	}
	return err
}
func (m *Manager) StorageStatus() (storagebudget.Status, error) {
	if m.budget == nil {
		return storagebudget.Status{}, errors.New("storage retention is not configured")
	}
	m.budget.Lock()
	defer m.budget.Unlock()
	return m.statusLocked()
}
func (m *Manager) statusLocked() (storagebudget.Status, error) {
	s, err := m.budget.StatusLocked()
	if err != nil {
		return s, err
	}
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.annotateStatusLocked(s)
}
func (m *Manager) annotateStatusLocked(s storagebudget.Status) (storagebudget.Status, error) {
	known := int64(0)
	for id, j := range m.jobs {
		n, e := m.knownJobSize(id)
		if e != nil {
			return s, e
		}
		known += n
		if j.State.Terminal() {
			s.CompletedJobs++
		} else {
			s.ActiveJobs++
		}
	}
	artifacts, e := m.artifacts.List()
	if e != nil {
		return s, e
	}
	for _, a := range artifacts {
		n, safe, e := m.artifacts.OwnedSize(a.ID)
		if e != nil {
			return s, e
		}
		if safe {
			known += n
		}
	}
	// Everything outside validated artifact/job directories is counted in the
	// budget but never selected for deletion (including durable tombstones).
	s.RecoveryLocations = m.recoveryLocations()
	s.UnknownBytes = s.UsedBytes - known
	if s.UnknownBytes < 0 {
		s.UnknownBytes = 0
	}
	return s, nil
}
func (m *Manager) Cleanup(apply bool) (CleanupResult, error) {
	if m.budget == nil {
		return CleanupResult{}, errors.New("storage retention is not configured")
	}
	m.budget.Lock()
	defer m.budget.Unlock()
	return m.cleanupLocked(apply, 0)
}

// cleanupLocked holds Gate before acquiring the job mutex. A persisted tombstone
// is also a prune journal: once durable, startup must never load its job again,
// even if the process died before the directory was removed.
func (m *Manager) cleanupLocked(apply bool, need int64) (CleanupResult, error) {
	result := CleanupResult{Apply: apply, JobIDs: []string{}, ArtifactIDs: []string{}}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	s, err := m.budget.StatusLocked()
	if err != nil {
		return result, err
	}
	completed := make([]*Job, 0)
	active := map[string]bool{}
	for id := range m.budget.Pins {
		active[id] = true
	}
	lastUsed := map[string]time.Time{}
	for _, j := range m.jobs {
		if j.State.Terminal() {
			completed = append(completed, j)
		} else {
			active[j.Request.ArtifactID] = true
		}
		if j.CreatedUTC.After(lastUsed[j.Request.ArtifactID]) {
			lastUsed[j.Request.ArtifactID] = j.CreatedUTC
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if completed[i].CreatedUTC.Equal(completed[j].CreatedUTC) {
			return completed[i].ID > completed[j].ID
		}
		return completed[i].CreatedUTC.After(completed[j].CreatedUTC)
	})
	prune := completed
	if len(prune) > completedJobLimit {
		prune = prune[completedJobLimit:]
	} else {
		prune = nil
	}
	// References from jobs that will be pruned do not protect an artifact. Active
	// references always win; completed references determine eviction priority.
	referenced := map[string]bool{}
	for i, j := range completed {
		if i < completedJobLimit {
			referenced[j.Request.ArtifactID] = true
		}
	}
	artifacts, err := m.artifacts.List()
	if err != nil {
		return result, err
	}
	sort.Slice(artifacts, func(i, j int) bool {
		a, b := artifacts[i], artifacts[j]
		if referenced[a.ID] != referenced[b.ID] {
			return !referenced[a.ID]
		}
		ta, tb := a.ImportedUTC, b.ImportedUTC
		if referenced[a.ID] {
			ta = lastUsed[a.ID]
		}
		if referenced[b.ID] {
			tb = lastUsed[b.ID]
		}
		if ta.Equal(tb) {
			return a.ID < b.ID
		}
		return ta.Before(tb)
	})
	used := s.UsedBytes
	// Leave maintenance headroom before writing a tombstone. The fixed reserve
	// also covers metadata replacement's temporary second copy.
	target := m.budget.Limit - s.ReservedBytes - need
	for _, j := range prune {
		n, e := m.knownJobSize(j.ID)
		if e != nil {
			return result, e
		}
		used -= n
		if j.Request.RequestID != "" {
			data, _ := json.Marshal(tombstone{j.ID, fingerprint(j.Request)})
			used += int64(len(data))
		}
		result.JobIDs = append(result.JobIDs, j.ID)
		result.ReclaimableBytes += n
		if apply {
			if j.Request.RequestID != "" {
				t := tombstone{j.ID, fingerprint(j.Request)}
				data, _ := json.Marshal(t)
				actual, e := m.budget.StatusLocked()
				if e != nil {
					return result, e
				}
				if actual.UsedBytes+int64(len(data))+4096 > m.budget.Limit {
					return result, storagebudget.ErrFull
				}
				if err := m.writeTombstone(j.Request.RequestID, data); err != nil {
					return result, err
				}
				m.expired[j.Request.RequestID] = t
				delete(m.submissions, j.Request.RequestID)
			}
			if err := m.removeKnownJob(j.ID); err != nil {
				return result, err
			}
			delete(m.jobs, j.ID)
			delete(m.events, j.ID)
			m.closeSubscribersLocked(j.ID)
		}
	}
	if apply {
		actual, e := m.budget.StatusLocked()
		if e != nil {
			return result, e
		}
		used = actual.UsedBytes
		target = m.budget.Limit - actual.ReservedBytes - need
	}
	for _, a := range artifacts {
		if used <= target {
			break
		}
		if active[a.ID] {
			continue
		}
		n, safe, e := m.artifacts.OwnedSize(a.ID)
		if e != nil {
			return result, e
		}
		if !safe {
			continue
		}
		result.ArtifactIDs = append(result.ArtifactIDs, a.ID)
		result.ReclaimableBytes += n
		used -= n
		if apply {
			if err := m.artifacts.Remove(a.ID); err != nil {
				return result, err
			}
		}
	}

	result.Status, err = m.budget.StatusLocked()
	if err != nil {
		return result, err
	}
	result.Status, err = m.annotateStatusLocked(result.Status)
	if err != nil {
		return result, err
	}
	return result, nil
}
func (m *Manager) writeTombstone(id string, data []byte) error {
	dir := filepath.Join(m.root, "submissions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := syncDirectory(m.root); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".submission-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return publishMetadata(f.Name(), filepath.Join(dir, id+".json"))
}
func (m *Manager) loadTombstones() error {
	dir := filepath.Join(m.root, "submissions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".submission-") {
			if m.budget != nil {
				if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
					return err
				}
			}
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !requestIDPattern.MatchString(id) || e.IsDir() {
			return fmt.Errorf("invalid submission tombstone")
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var t tombstone
		if err := json.Unmarshal(data, &t); err != nil || !requestIDPattern.MatchString(t.JobID) || len(t.Fingerprint) != 64 {
			return fmt.Errorf("invalid submission tombstone %s", id)
		}
		if _, err := hex.DecodeString(t.Fingerprint); err != nil {
			return err
		}
		m.expired[id] = t
	}
	return nil
}
func (m *Manager) retentionAfterCompletion() {
	if m.budget == nil {
		return
	}
	m.budget.Lock()
	defer m.budget.Unlock()
	m.mutex.RLock()
	for id, j := range m.jobs {
		if j.State.Terminal() {
			delete(m.budget.Reservations, id)
			delete(m.budget.Consumption, id)
		}
	}
	m.mutex.RUnlock()
	// A failed cleanup is retried on next growth or operator request. Accepted
	// terminal state remains durable regardless of retention failure.
	if _, err := m.cleanupLocked(true, 0); err != nil {
		log.Printf("storage cleanup failed: %v", err)
	}
}
func (m *Manager) inspectJob(job *Job) Job {
	result := cloneJob(job)
	if m.artifacts == nil {
		return result
	}
	_, err := m.artifacts.Load(result.Request.ArtifactID)
	result.ArtifactAvailable = err == nil
	return result
}

func (m *Manager) knownJobSize(id string) (int64, error) {
	var n int64
	for _, name := range []string{"job.json", "events.jsonl", serialTranscriptName} {
		info, err := os.Lstat(filepath.Join(m.root, id, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return n, err
		}
		if info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n, nil
}
func (m *Manager) removeKnownJob(id string) error {
	dir := filepath.Join(m.root, id)
	for _, name := range []string{"job.json", "events.jsonl", serialTranscriptName} {
		p := filepath.Join(dir, name)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	// Leave directories containing unknown state intact.
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(dir)
	}
	return nil
}

func (m *Manager) recoveryLocations() []storagebudget.RecoveryLocation {
	out := []storagebudget.RecoveryLocation{}
	add := func(path string) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			out = append(out, storagebudget.RecoveryLocation{Path: path, Inaccessible: true})
			return
		}
		if !info.IsDir() {
			return
		}
		n, err := storagebudget.Size(path)
		out = append(out, storagebudget.RecoveryLocation{Path: path, Bytes: n, Inaccessible: err != nil})
	}
	root := filepath.Clean(m.budget.Root)
	legacy := filepath.Join(filepath.Dir(root), "mnc-station")
	if legacy != root {
		add(legacy)
	}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/var/backups")
		if err != nil && !os.IsNotExist(err) {
			out = append(out, storagebudget.RecoveryLocation{Path: "/var/backups", Inaccessible: true})
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "mps-migration-") {
				add(filepath.Join("/var/backups", e.Name()))
			}
		}
	}
	return out
}

// Startup bounds legacy output without following links. Keep a prefix plus the
// final event's original sequence so cursors remain monotonic after truncation.
func (m *Manager) boundHistoricalOutput(job *Job) error {
	if m.budget == nil {
		return nil
	}
	changed := false
	path := filepath.Join(m.root, job.ID, "events.jsonl")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > eventLimitBytes {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		prefix, err := io.ReadAll(io.LimitReader(f, eventLimitBytes-(32<<10)))
		if err != nil {
			f.Close()
			return err
		}
		if i := bytes.LastIndexByte(prefix, '\n'); i >= 0 {
			prefix = prefix[:i+1]
		} else {
			prefix = nil
		}
		offset := info.Size() - (32 << 10)
		if offset < 0 {
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		tail, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return err
		}
		lines := bytes.Split(bytes.TrimSpace(tail), []byte{'\n'})
		var last Event
		if len(lines) > 0 {
			_ = json.Unmarshal(lines[len(lines)-1], &last)
		}
		if last.Sequence == 0 {
			last = Event{Sequence: job.EventCount, Time: time.Now().UTC(), Level: "warning", Message: "Historical events truncated by storage retention"}
		}
		last.Message = boundedText(last.Message, 2048)
		data, _ := json.Marshal(last)
		prefix = append(prefix, append(data, '\n')...)
		// Opening an existing validated regular file with TRUNC uses no second copy.
		if err := os.WriteFile(path, prefix, 0600); err != nil {
			return err
		}
		job.EventsTruncated = true
		changed = true
	}
	if job.SerialCapture != nil && m.maxConsoleLogBytes > 0 {
		path = filepath.Join(m.root, job.ID, serialTranscriptName)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > m.maxConsoleLogBytes {
			if err := os.Truncate(path, m.maxConsoleLogBytes); err != nil {
				return err
			}
			job.SerialCapture.Truncated = true
			job.SerialCapture.RetainedBytes = m.maxConsoleLogBytes
			changed = true
		}
	}
	if changed {
		return m.persistLocked(job)
	}
	return nil
}
