// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package xsdb

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const targetMarker = "MNC_XSDB_TARGET"

type Target struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DeviceIndex string `json:"deviceIndex,omitempty"`
	DeviceName  string `json:"deviceName,omitempty"`
	CableName   string `json:"cableName,omitempty"`
	CableSerial string `json:"cableSerial,omitempty"`
}

func (executor Executor) Discover(ctx context.Context, hardwareServerURL string) ([]Target, error) {
	if err := ValidateHWServerURL(hardwareServerURL); err != nil {
		return nil, err
	}
	path, err := executor.Resolve()
	if err != nil {
		return nil, err
	}
	script, err := os.CreateTemp("", "mnc-xsdb-targets-*.tcl")
	if err != nil {
		return nil, fmt.Errorf("create XSDB target discovery script: %w", err)
	}
	scriptPath := script.Name()
	defer os.Remove(scriptPath)
	if _, err := script.WriteString(targetDiscoveryTCL); err != nil {
		script.Close()
		return nil, fmt.Errorf("write XSDB target discovery script: %w", err)
	}
	if err := script.Close(); err != nil {
		return nil, fmt.Errorf("close XSDB target discovery script: %w", err)
	}

	output, err := newCommand(ctx, path, scriptPath, hardwareServerURL).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 4096 {
			message = message[len(message)-4096:]
		}
		if message == "" {
			return nil, fmt.Errorf("discover XSDB targets: %w", err)
		}
		return nil, fmt.Errorf("discover XSDB targets: %w: %s", err, message)
	}
	return parseTargets(output)
}

func parseTargets(output []byte) ([]Target, error) {
	targets := make([]Target, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSuffix(scanner.Text(), "\r"), "\t")
		if len(fields) == 0 || fields[0] != targetMarker {
			continue
		}
		if len(fields) != 7 {
			return nil, fmt.Errorf("XSDB returned a malformed target record")
		}
		values := make([]string, 6)
		for index, encoded := range fields[1:] {
			decoded, err := hex.DecodeString(encoded)
			if err != nil || !utf8.Valid(decoded) {
				return nil, fmt.Errorf("XSDB returned an invalid target property")
			}
			values[index] = string(decoded)
		}
		if err := ValidateTargetID(values[0]); err != nil {
			return nil, fmt.Errorf("XSDB returned an invalid target: %w", err)
		}
		targets = append(targets, Target{
			ID: values[0], Name: values[1], DeviceIndex: values[2],
			DeviceName: values[3], CableName: values[4], CableSerial: values[5],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read XSDB target discovery output: %w", err)
	}
	return targets, nil
}

const targetDiscoveryTCL = `
proc mnc_hex_property {target key} {
    if { ![dict exists $target $key] } {
        return ""
    }
    return [binary encode hex [encoding convertto utf-8 [dict get $target $key]]]
}

if { [llength $argv] != 1 } {
    error "Usage: target discovery <hw-server-url>"
}

connect -url [lindex $argv 0]
foreach target [targets -target-properties -nocase -filter {name =~ "*PSU*"}] {
    set fields {}
    foreach key {target_id name jtag_device_index jtag_device_name jtag_cable_name jtag_cable_serial} {
        lappend fields [mnc_hex_property $target $key]
    }
    puts "MNC_XSDB_TARGET\t[join $fields \t]"
}
disconnect
`
