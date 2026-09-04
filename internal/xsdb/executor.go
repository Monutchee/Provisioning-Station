// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package xsdb resolves the Xilinx debug tools and executes XSDB without
// linking any Vivado libraries into the Station agent.
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
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Executor struct {
	Path string
}

type Request struct {
	Entrypoint        string
	HWServerURL       string
	TFTPServerIP      string
	BoardIP           string
	TargetID          string
	TargetCableSerial string
	TargetDeviceIndex string
	WorkingFolder     string
}

type LogFunc func(line string)

func (executor Executor) Resolve() (string, error) {
	return executor.resolve(defaultInstallRoots())
}

func (executor Executor) resolve(installRoots []string) (string, error) {
	return resolveXilinxTool(executor.Path, "MNC_XSDB", "xsdb-path", "xsdb", installRoots)
}

// ResolveHWServer locates the Xilinx hardware server executable. An explicit
// path takes precedence over MNC_HW_SERVER and automatic discovery.
func ResolveHWServer(path string) (string, error) {
	return resolveHWServer(path, defaultInstallRoots())
}

func resolveHWServer(path string, installRoots []string) (string, error) {
	return resolveXilinxTool(path, "MNC_HW_SERVER", "hw-server-path", "hw_server", installRoots)
}

func resolveXilinxTool(path, overrideEnvironment, option, name string, installRoots []string) (string, error) {
	if path != "" {
		return resolveCandidate(path, name)
	}
	if value := os.Getenv(overrideEnvironment); value != "" {
		return resolveCandidate(value, name)
	}
	if path, err := exec.LookPath(executableName(name)); err == nil {
		return filepath.Abs(path)
	}
	for _, environment := range []string{"XILINX_VITIS", "XILINX_VIVADO"} {
		root := os.Getenv(environment)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, "bin", executableName(name))
		if path, err := resolveCandidate(candidate, name); err == nil {
			return path, nil
		}
	}
	for _, root := range installRoots {
		if path, err := findToolInInstallRoot(root, name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf(
		"%s was not found in PATH, XILINX_VITIS/XILINX_VIVADO, or the default install locations %s; set --%s or %s to override discovery",
		name, strings.Join(installRoots, ", "), option, overrideEnvironment,
	)
}

func defaultInstallRoots() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\Xilinx`}
	}
	return []string{"/opt/Xilinx"}
}

func findToolInInstallRoot(root, tool string) (string, error) {
	name := executableName(tool)
	patterns := []string{
		filepath.Join(root, "Vitis", "*", "bin", name),
		filepath.Join(root, "*", "Vitis", "bin", name),
		filepath.Join(root, "Vivado", "*", "bin", name),
		filepath.Join(root, "*", "Vivado", "bin", name),
		filepath.Join(root, "HWSRVR", "*", "bin", name),
		filepath.Join(root, "*", "HWSRVR", "bin", name),
		filepath.Join(root, "bin", name),
	}
	var candidates []string
	seen := make(map[string]struct{})
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", fmt.Errorf("search Xilinx install root %s: %w", root, err)
		}
		for _, candidate := range matches {
			if _, exists := seen[candidate]; exists {
				continue
			}
			if _, err := resolveCandidate(candidate, tool); err != nil {
				continue
			}
			seen[candidate] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("%s was not found below %s", tool, root)
	}
	sort.SliceStable(candidates, func(first, second int) bool {
		return compareVersions(candidateVersion(candidates[first], root), candidateVersion(candidates[second], root)) > 0
	})
	return filepath.Abs(candidates[0])
}

func candidateVersion(candidate, root string) []int {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return nil
	}
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		parts := strings.Split(component, ".")
		if len(parts) < 2 {
			continue
		}
		version := make([]int, len(parts))
		valid := true
		for index, part := range parts {
			value, parseErr := strconv.Atoi(part)
			if parseErr != nil {
				valid = false
				break
			}
			version[index] = value
		}
		if valid {
			return version
		}
	}
	return nil
}

func compareVersions(first, second []int) int {
	length := len(first)
	if len(second) > length {
		length = len(second)
	}
	for index := 0; index < length; index++ {
		var firstPart, secondPart int
		if index < len(first) {
			firstPart = first[index]
		}
		if index < len(second) {
			secondPart = second[index]
		}
		if firstPart < secondPart {
			return -1
		}
		if firstPart > secondPart {
			return 1
		}
	}
	return 0
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
	if err := ValidateTargetID(request.TargetID); err != nil {
		return err
	}
	if err := ValidateTargetCableSerial(request.TargetCableSerial); err != nil {
		return err
	}
	if err := ValidateTargetDeviceIndex(request.TargetDeviceIndex); err != nil {
		return err
	}
	if request.TargetDeviceIndex != "" && request.TargetCableSerial == "" {
		return fmt.Errorf("XSDB target device index requires a target cable serial")
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
	if request.BoardIP != "" || request.TargetID != "" || request.TargetCableSerial != "" {
		arguments = append(arguments, request.BoardIP)
	}
	if request.TargetID != "" || request.TargetCableSerial != "" {
		arguments = append(arguments, request.TargetID)
	}
	if request.TargetCableSerial != "" {
		arguments = append(arguments, request.TargetCableSerial)
	}
	if request.TargetDeviceIndex != "" {
		arguments = append(arguments, request.TargetDeviceIndex)
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

func ValidateTargetID(value string) error {
	if value == "" {
		return nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 || strconv.Itoa(id) != value {
		return fmt.Errorf("XSDB target ID must be a positive decimal integer: %q", value)
	}
	return nil
}

func ValidateTargetCableSerial(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("JTAG cable serial must be a single line of at most 256 characters")
	}
	return nil
}

func ValidateTargetDeviceIndex(value string) error {
	if value == "" {
		return nil
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 || strconv.Itoa(index) != value {
		return fmt.Errorf("JTAG device index must be a non-negative decimal integer: %q", value)
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

func resolveCandidate(candidate, name string) (string, error) {
	if !filepath.IsAbs(candidate) && !strings.ContainsRune(candidate, os.PathSeparator) {
		path, err := exec.LookPath(candidate)
		if err != nil {
			return "", fmt.Errorf("find %s executable %q: %w", name, candidate, err)
		}
		candidate = path
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s executable: %w", name, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect %s executable %s: %w", name, absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s executable is not a regular file: %s", name, absolute)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s executable is not executable: %s", name, absolute)
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
