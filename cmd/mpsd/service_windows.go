//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
)

func machineDataDir() (string, error) {
	root := os.Getenv("ProgramData")
	if root == "" {
		return "", fmt.Errorf("ProgramData is unavailable")
	}
	return filepath.Join(root, "Monutchee", "MPS"), nil
}
func runService(arguments []string) error {
	if len(arguments) != 0 {
		return fmt.Errorf("service accepts no arguments; configure MPS environment settings")
	}
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !service {
		return fmt.Errorf("service must be launched by Windows SCM; use mpsd serve for foreground mode")
	}
	return svc.Run("mps", &stationService{})
}

type stationService struct{}

func (*stationService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 60000}
	config, err := defaultServeConfig()
	if err != nil {
		return true, 1
	}
	config.OwnHWServer = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- serveContext(ctx, config, func() { close(ready) }) }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	checkpoint := uint32(1)
	running := false
	stopping := false
	for {
		select {
		case <-ready:
			ready = nil
			running = true
			if !stopping {
				status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			}
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				stopping = true
				cancel()
				status <- svc.Status{State: svc.StopPending, WaitHint: 30000}
			}
		case <-ticker.C:
			checkpoint++
			if stopping {
				status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 30000}
			} else if !running {
				status <- svc.Status{State: svc.StartPending, CheckPoint: checkpoint, WaitHint: 60000}
			}
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		}
	}
}
