// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package xsdb resolves and executes the Xilinx XSDB tool without linking any
// Vivado libraries into the Station agent.
package xsdb

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type Executor struct {
	Path string
}

type Request struct {
	Entrypoint    string
	HWServerURL   string
	TFTPServerIP  string
	BoardIP       string
	WorkingFolder string
}

type LogFunc func(line string)

func (executor Executor) Resolve() (string, error) {
	if executor.Path != "" {
		return resolveCandidate(executor.Path)
	}
	if value := os.Getenv("MNC_XSDB"); value != "" {
		return resolveCandidate(value)
	}
	if path, err := exec.LookPath(executableName("xsdb")); err == nil {
		return filepath.Abs(path)
	}
	for _, environment := range []string{"XILINX_VITIS", "XILINX_VIVADO"} {
		root := os.Getenv(environment)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, "bin", executableName("xsdb"))
		if path, err := resolveCandidate(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("xsdb was not found; set --xsdb-path, MNC_XSDB, or the Xilinx environment")
}

func (executor Executor) Run(ctx context.Context, request Request, emit LogFunc) error {
	path, err := executor.Resolve()
	if err != nil {
		return err
	}
	if err := ValidateHWServerURL(request.HWServerURL); err != nil {
		return err
	}
	if err := ValidateIPv4("TFTP server IP", request.TFTPServerIP, false); err != nil {
		return err
	}
	if err := ValidateIPv4("board IP", request.BoardIP, true); err != nil {
		return err
	}
	entrypoint, err := filepath.Abs(request.Entrypoint)
	if err != nil {
		return fmt.Errorf("resolve XSDB entrypoint: %w", err)
	}
	info, err := os.Stat(entrypoint)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("XSDB entrypoint is not a regular file: %s", entrypoint)
	}

	arguments := []string{entrypoint, request.HWServerURL, request.TFTPServerIP}
	if request.BoardIP != "" {
		arguments = append(arguments, request.BoardIP)
	}
	command := newCommand(ctx, path, arguments...)
	if request.WorkingFolder != "" {
		command.Dir = request.WorkingFolder
	}
	writer := newLineWriter(emit)
	command.Stdout = writer
	command.Stderr = writer
	if emit != nil {
		emit(fmt.Sprintf("Starting XSDB: %s", path))
	}
	if err := command.Run(); err != nil {
		writer.Flush()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("xsdb failed: %w", err)
	}
	writer.Flush()
	return nil
}

func ValidateHWServerURL(value string) error {
	if !strings.HasPrefix(value, "tcp:") || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("hw_server URL must use tcp:<host>:<port>")
	}
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(value, "tcp:"))
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("hw_server URL must use tcp:<host>:<port>: %q", value)
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return fmt.Errorf("hw_server URL has an invalid host: %q", value)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("hw_server URL has an invalid port: %q", value)
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' && character != '_' {
				return false
			}
		}
	}
	return true
}

func ValidateIPv4(label, value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	parsed := net.ParseIP(value)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("%s is not an IPv4 address: %q", label, value)
	}
	return nil
}

func resolveCandidate(candidate string) (string, error) {
	if !filepath.IsAbs(candidate) && !strings.ContainsRune(candidate, os.PathSeparator) {
		path, err := exec.LookPath(candidate)
		if err != nil {
			return "", fmt.Errorf("find xsdb executable %q: %w", candidate, err)
		}
		candidate = path
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve xsdb executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("xsdb executable is not a regular file: %s", absolute)
	}
	return absolute, nil
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".bat"
	}
	return name
}

type lineWriter struct {
	mutex sync.Mutex
	data  bytes.Buffer
	emit  LogFunc
}

func newLineWriter(emit LogFunc) *lineWriter {
	return &lineWriter{emit: emit}
}

func (writer *lineWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	written := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			_, _ = writer.data.Write(data)
			break
		}
		_, _ = writer.data.Write(data[:index])
		writer.emitBuffered()
		data = data[index+1:]
	}
	return written, nil
}

func (writer *lineWriter) Flush() {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.data.Len() > 0 {
		writer.emitBuffered()
	}
}

func (writer *lineWriter) emitBuffered() {
	line := strings.TrimSuffix(writer.data.String(), "\r")
	writer.data.Reset()
	if writer.emit != nil {
		writer.emit(line)
	}
}
