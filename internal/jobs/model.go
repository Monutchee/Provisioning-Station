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
	ArtifactID        string `json:"artifactId"`
	HWServerURL       string `json:"hwServerUrl"`
	TFTPServerIP      string `json:"tftpServerIp"`
	BoardIP           string `json:"boardIp,omitempty"`
	TargetID          string `json:"targetId,omitempty"`
	TargetCableSerial string `json:"targetCableSerial,omitempty"`
	TargetDeviceIndex string `json:"targetDeviceIndex,omitempty"`
}

type Job struct {
	ID          string     `json:"id"`
	Request     Request    `json:"request"`
	State       State      `json:"state"`
	CreatedUTC  time.Time  `json:"createdUtc"`
	StartedUTC  *time.Time `json:"startedUtc,omitempty"`
	FinishedUTC *time.Time `json:"finishedUtc,omitempty"`
	Error       string     `json:"error,omitempty"`
	EventCount  int        `json:"eventCount"`
}

type Event struct {
	Sequence int       `json:"sequence"`
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}
