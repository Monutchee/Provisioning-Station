// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import "time"

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

func (state State) Terminal() bool {
	return state == StateSucceeded || state == StateFailed || state == StateCanceled
}

type Request struct {
	ArtifactID        string                `json:"artifactId"`
	HWServerURL       string                `json:"hwServerUrl"`
	TFTPServerIP      string                `json:"tftpServerIp"`
	BoardIP           string                `json:"boardIp,omitempty"`
	TargetID          string                `json:"targetId,omitempty"`
	TargetCableSerial string                `json:"targetCableSerial,omitempty"`
	TargetDeviceIndex string                `json:"targetDeviceIndex,omitempty"`
	SerialConsole     *SerialConsoleRequest `json:"serialConsole,omitempty"`
}

type SerialConsoleRequest struct {
	PortID   string `json:"portId"`
	BaudRate int    `json:"baudRate,omitempty"`
}

type SerialCapture struct {
	State         string     `json:"state"`
	ReceivedBytes int64      `json:"receivedBytes"`
	RetainedBytes int64      `json:"retainedBytes"`
	Truncated     bool       `json:"truncated"`
	StartedUTC    time.Time  `json:"startedUtc"`
	FinishedUTC   *time.Time `json:"finishedUtc,omitempty"`
}

type Job struct {
	ID            string         `json:"id"`
	Request       Request        `json:"request"`
	State         State          `json:"state"`
	CreatedUTC    time.Time      `json:"createdUtc"`
	StartedUTC    *time.Time     `json:"startedUtc,omitempty"`
	FinishedUTC   *time.Time     `json:"finishedUtc,omitempty"`
	Error         string         `json:"error,omitempty"`
	EventCount    int            `json:"eventCount"`
	SerialCapture *SerialCapture `json:"serialCapture,omitempty"`
}

type Event struct {
	Sequence int       `json:"sequence"`
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}

func cloneRequest(request Request) Request {
	result := request
	if request.SerialConsole != nil {
		serialRequest := *request.SerialConsole
		result.SerialConsole = &serialRequest
	}
	return result
}

func cloneJob(job *Job) Job {
	result := *job
	result.Request = cloneRequest(job.Request)
	if job.SerialCapture != nil {
		capture := *job.SerialCapture
		result.SerialCapture = &capture
	}
	return result
}
