//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package servicecontrol

import (
	"context"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func connect(ctx context.Context, access uint32) (*mgr.Mgr, *mgr.Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	handle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, err
	}
	m := &mgr.Mgr{Handle: handle}
	name, err := windows.UTF16PtrFromString(Name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	service, err := windows.OpenService(handle, name, access)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	s := &mgr.Service{Name: Name, Handle: service}
	return m, s, nil
}
func Start(ctx context.Context) error {
	m, s, err := connect(ctx, windows.SERVICE_QUERY_STATUS|windows.SERVICE_START)
	if err != nil {
		return actionError("start (run sc.exe start mps from an administrator terminal if authorization is required)", err)
	}
	defer m.Disconnect()
	defer s.Close()
	state, err := s.Query()
	if err == nil && (state.State == svc.Running || state.State == svc.StartPending) {
		return nil
	}
	if err = s.Start(); err != nil {
		return actionError("start (run sc.exe start mps from an administrator terminal if authorization is required)", err)
	}
	return nil
}
func Stop(ctx context.Context) error {
	m, s, err := connect(ctx, windows.SERVICE_QUERY_STATUS|windows.SERVICE_STOP)
	if err != nil {
		return actionError("stop (run sc.exe stop mps from an administrator terminal if authorization is required)", err)
	}
	defer m.Disconnect()
	defer s.Close()
	state, err := s.Query()
	if err == nil && state.State == svc.Stopped {
		return nil
	}
	_, err = s.Control(svc.Stop)
	if err != nil {
		return actionError("stop (run sc.exe stop mps from an administrator terminal if authorization is required)", err)
	}
	return nil
}
func Query(ctx context.Context) (Status, error) {
	m, s, err := connect(ctx, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return Status{}, err
	}
	defer m.Disconnect()
	defer s.Close()
	state, err := s.Query()
	if err != nil {
		return Status{}, err
	}
	names := map[svc.State]string{svc.Running: "active", svc.Stopped: "inactive", svc.StartPending: "activating", svc.StopPending: "deactivating", svc.Paused: "paused"}
	return Status{State: names[state.State]}, nil
}
