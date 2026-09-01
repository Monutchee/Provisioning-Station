// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package serialconsole

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.bug.st/serial/enumerator"
)

func nativeCandidates() ([]candidate, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	result := make([]candidate, 0)
	for _, port := range ports {
		if !port.IsUSB || !strings.EqualFold(port.VID, FTDIVendorID) || !strings.EqualFold(port.PID, FT2232HProductID) {
			continue
		}
		channel, err := linuxUSBChannel(port.Name)
		if err != nil || channel != FT2232HUARTChannel {
			continue
		}
		result = append(result, candidate{
			Name: port.Name, VendorID: FTDIVendorID, ProductID: FT2232HProductID,
			USBSerial: port.SerialNumber, Channel: channel,
		})
	}
	return result, nil
}

func linuxUSBChannel(portName string) (string, error) {
	path, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(portName), "device"))
	if err != nil {
		return "", err
	}
	for {
		data, readErr := os.ReadFile(filepath.Join(path, "bInterfaceNumber"))
		if readErr == nil {
			value := strings.TrimSpace(string(data))
			switch value {
			case "00":
				return "A", nil
			case "01":
				return "B", nil
			default:
				return "", fmt.Errorf("unsupported FTDI USB interface %q", value)
			}
		}
		parent := filepath.Dir(path)
		if parent == path || path == "/sys" {
			return "", fmt.Errorf("USB interface was not found for %s", portName)
		}
		path = parent
	}
}
