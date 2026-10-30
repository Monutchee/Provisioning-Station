// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/tasks"
)

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
	DeploymentTasks   *tasks.Plan           `json:"deploymentTasks,omitempty"`
	ArtifactID        string                `json:"artifactId"`
	HWServerURL       string                `json:"hwServerUrl"`
	TFTPServerIP      string                `json:"tftpServerIp"`
	BoardIP           string                `json:"boardIp,omitempty"`
	TargetID          string                `json:"targetId,omitempty"`
	TargetCableSerial string                `json:"targetCableSerial,omitempty"`
	TargetDeviceIndex string                `json:"targetDeviceIndex,omitempty"`
	SerialConsole     *SerialConsoleRequest `json:"serialConsole,omitempty"`
}

type SerialSelection string

const (
	SerialSelectionMatched SerialSelection = "matched"
	SerialSelectionManual  SerialSelection = "manual"
)

type SerialConsoleRequest struct {
	PortID    string          `json:"portId"`
	BaudRate  int             `json:"baudRate,omitempty"`
	Selection SerialSelection `json:"selection,omitempty"`
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
	TaskResults       []tasks.Result `json:"taskResults,omitempty"`
	DeploymentError   string         `json:"deploymentError,omitempty"`
	DeploymentOutcome string         `json:"deploymentOutcome,omitempty"`
	TaskErrors        []string       `json:"taskErrors,omitempty"`
	ID                string         `json:"id"`
	Request           Request        `json:"request"`
	State             State          `json:"state"`
	CreatedUTC        time.Time      `json:"createdUtc"`
	StartedUTC        *time.Time     `json:"startedUtc,omitempty"`
	FinishedUTC       *time.Time     `json:"finishedUtc,omitempty"`
	Error             string         `json:"error,omitempty"`
	EventCount        int            `json:"eventCount"`
	SerialCapture     *SerialCapture `json:"serialCapture,omitempty"`
}

type Event struct {
	Sequence int       `json:"sequence"`
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}

func cloneRequest(request Request) Request {
	result := request
	result.DeploymentTasks = request.DeploymentTasks.Clone()
	if request.SerialConsole != nil {
		serialRequest := *request.SerialConsole
		result.SerialConsole = &serialRequest
	}
	return result
}

func cloneJob(job *Job) Job {
	result := *job
	result.TaskResults = append([]tasks.Result(nil), job.TaskResults...)
	for i := range result.TaskResults {
		if result.TaskResults[i].FinishedUTC != nil {
			stamp := *result.TaskResults[i].FinishedUTC
			result.TaskResults[i].FinishedUTC = &stamp
		}
	}
	result.TaskErrors = append([]string(nil), job.TaskErrors...)
	result.Request = cloneRequest(job.Request)
	if job.SerialCapture != nil {
		capture := *job.SerialCapture
		result.SerialCapture = &capture
	}
	return result
}
