//go:build windows

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
	"golang.org/x/sys/windows"
)

// startOwnedHWServer keeps optional vendor processes inside a kill-on-close job.
// No vendor installation is required to run the dashboard and HTTP API.
func startOwnedHWServer(ctx context.Context) (func(), error) {
	path, err := xsdb.ResolveHWServer(os.Getenv("MPS_HW_SERVER"))
	if err != nil {
		return func() {}, nil
	}
	listen := os.Getenv("MPS_HW_SERVER_LISTEN")
	if listen == "" {
		listen = "tcp:127.0.0.1:3121"
	}
	if err := xsdb.ValidateHWServerURL(listen); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	// Start suspended so descendants cannot escape the job before assignment.
	cmd := HardwareServerCommand(path, []string{"-s" + listen})
	attributes := syscallSysProcAttr
	cmd.SysProcAttr = &attributes
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
	}
	if err == nil {
		err = resumeProcess(uint32(cmd.Process.Pid))
	}
	if process != 0 {
		windows.CloseHandle(process)
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return func() { windows.CloseHandle(job); <-done }, nil
}

var syscallSysProcAttr = syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}

// Resume the suspended primary thread only after assigning the process to its job.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("find suspended hardware-server thread: %w", err)
}
func HardwareServerCommand(path string, arguments []string) *exec.Cmd {
	var command *exec.Cmd
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".bat" || extension == ".cmd" {
		interpreter := os.Getenv("COMSPEC")
		if interpreter == "" {
			interpreter = "cmd.exe"
		}
		commandArguments := []string{"/d", "/s", "/v:off", "/c", path}
		command = exec.Command(interpreter, append(commandArguments, arguments...)...)
	} else {
		command = exec.Command(path, arguments...)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command
}
