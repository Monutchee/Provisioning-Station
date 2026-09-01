// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
)

const serialTranscriptName = "serial-console.bin"

type activeSerialCapture struct {
	attachment *serialconsole.Attachment
	file       *os.File
	limit      int64
	emit       func(string, string)

	stop     chan struct{}
	done     chan struct{}
	failure  chan error
	stopOnce sync.Once

	mutex      sync.Mutex
	metadata   SerialCapture
	writeErr   error
	failureErr error
}

func (manager *Manager) startSerialCapture(ctx context.Context, id string, request *SerialConsoleRequest) (*activeSerialCapture, error) {
	if request == nil {
		return nil, nil
	}
	if manager.console == nil {
		return nil, fmt.Errorf("serial console capture is not configured")
	}
	path := filepath.Join(manager.root, id, serialTranscriptName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create serial transcript: %w", err)
	}
	attachment, err := manager.console.Attach(ctx, serialconsole.AttachRequest{
		PortID: request.PortID, BaudRate: request.BaudRate,
		Access: serialconsole.AccessObserver, Replay: false,
	})
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("start serial console capture: %w", err)
	}
	capture := &activeSerialCapture{
		attachment: attachment, file: file, limit: manager.maxConsoleLogBytes,
		emit: func(level, message string) { manager.emit(id, level, message) },
		stop: make(chan struct{}), done: make(chan struct{}), failure: make(chan error, 1),
		metadata: SerialCapture{State: "capturing", StartedUTC: time.Now().UTC()},
	}
	go capture.run()
	return capture, nil
}

func (capture *activeSerialCapture) run() {
	defer close(capture.done)
	warned := false
	for {
		select {
		case <-capture.stop:
			return
		case data, open := <-capture.attachment.Data():
			if !open {
				select {
				case <-capture.stop:
					return
				default:
				}
				if err := capture.attachment.Err(); err != nil {
					capture.fail(fmt.Errorf("serial console capture stopped: %w", err))
				}
				return
			}
			capture.mutex.Lock()
			capture.metadata.ReceivedBytes += int64(len(data))
			remaining := capture.limit - capture.metadata.RetainedBytes
			if remaining > 0 {
				toWrite := data
				if int64(len(toWrite)) > remaining {
					toWrite = toWrite[:remaining]
				}
				written, err := capture.file.Write(toWrite)
				capture.metadata.RetainedBytes += int64(written)
				if err != nil || written != len(toWrite) {
					if err == nil {
						err = fmt.Errorf("short write")
					}
					capture.writeErr = fmt.Errorf("write serial transcript: %w", err)
				}
			}
			if capture.metadata.ReceivedBytes > capture.limit {
				capture.metadata.Truncated = true
			}
			writeErr := capture.writeErr
			truncated := capture.metadata.Truncated
			capture.mutex.Unlock()
			if writeErr != nil {
				capture.fail(writeErr)
				return
			}
			if truncated && !warned {
				warned = true
				capture.emit("warning", fmt.Sprintf("Serial transcript reached the %d-byte retention limit; live output continues", capture.limit))
			}
		}
	}
}

func (capture *activeSerialCapture) fail(err error) {
	capture.mutex.Lock()
	if capture.failureErr == nil {
		capture.failureErr = err
	}
	capture.mutex.Unlock()
	select {
	case capture.failure <- err:
	default:
	}
}

func (capture *activeSerialCapture) Snapshot() SerialCapture {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return capture.metadata
}

func (capture *activeSerialCapture) Failure() <-chan error {
	return capture.failure
}

func (capture *activeSerialCapture) Stop(failed bool) (SerialCapture, error) {
	capture.stopOnce.Do(func() {
		close(capture.stop)
		capture.attachment.Close()
	})
	<-capture.done
	closeErr := capture.file.Close()
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	finished := time.Now().UTC()
	capture.metadata.FinishedUTC = &finished
	switch {
	case failed || capture.writeErr != nil || capture.failureErr != nil:
		capture.metadata.State = "failed"
	case capture.metadata.Truncated:
		capture.metadata.State = "truncated"
	default:
		capture.metadata.State = "complete"
	}
	if capture.failureErr != nil {
		return capture.metadata, capture.failureErr
	}
	if capture.writeErr != nil {
		return capture.metadata, capture.writeErr
	}
	return capture.metadata, closeErr
}

func (manager *Manager) SerialTranscript(id string) (string, SerialCapture, error) {
	manager.mutex.RLock()
	job := manager.jobs[id]
	if job == nil {
		manager.mutex.RUnlock()
		return "", SerialCapture{}, fmt.Errorf("job %s was not found", id)
	}
	if job.SerialCapture == nil {
		manager.mutex.RUnlock()
		return "", SerialCapture{}, fmt.Errorf("job %s has no serial transcript", id)
	}
	metadata := *job.SerialCapture
	manager.mutex.RUnlock()
	path := filepath.Join(manager.root, id, serialTranscriptName)
	if _, err := os.Stat(path); err != nil {
		return "", SerialCapture{}, fmt.Errorf("read serial transcript: %w", err)
	}
	return path, metadata, nil
}
