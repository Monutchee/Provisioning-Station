// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package serialconsole discovers and multiplexes local serial consoles.
package serialconsole

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	FTDIVendorID       = "0403"
	FT2232HProductID   = "6010"
	FT2232HUARTChannel = "B"
	DefaultBaudRate    = 115200
	DefaultReplayBytes = 256 << 10
	DefaultLogBytes    = 16 << 20
)

var (
	ErrPortNotFound   = errors.New("serial console port was not found")
	ErrPortAmbiguous  = errors.New("serial console identity is ambiguous")
	ErrControllerBusy = errors.New("serial console already has a controller")
	ErrModeConflict   = errors.New("serial console is already open at another baud rate")
	ErrSlowConsumer   = errors.New("serial console consumer could not keep up")
)

type Port struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	VendorID       string `json:"vendorId"`
	ProductID      string `json:"productId"`
	USBSerial      string `json:"usbSerial"`
	Channel        string `json:"channel"`
	Busy           bool   `json:"busy"`
	HasController  bool   `json:"hasController"`
	ActiveBaudRate int    `json:"activeBaudRate,omitempty"`
}

type Warning struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	USBSerial string `json:"usbSerial,omitempty"`
}

type Discovery struct {
	Ports    []Port    `json:"ports"`
	Warnings []Warning `json:"warnings"`
}

type candidate struct {
	Name      string
	VendorID  string
	ProductID string
	USBSerial string
	Channel   string
}

func newPort(value candidate) Port {
	identity := strings.Join([]string{
		"mnc-serial-console-v1", value.VendorID, value.ProductID, value.USBSerial, value.Channel,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return Port{
		ID:        hex.EncodeToString(digest[:]),
		Name:      value.Name,
		VendorID:  value.VendorID,
		ProductID: value.ProductID,
		USBSerial: value.USBSerial,
		Channel:   value.Channel,
	}
}

func normalizeCandidates(values []candidate) Discovery {
	counts := make(map[string]int)
	for _, value := range values {
		if value.USBSerial != "" {
			counts[value.USBSerial]++
		}
	}
	result := Discovery{Ports: make([]Port, 0, len(values)), Warnings: make([]Warning, 0)}
	for _, value := range values {
		switch {
		case value.USBSerial == "":
			result.Warnings = append(result.Warnings, Warning{
				Code: "serial_missing", Message: fmt.Sprintf("%s has no FTDI EEPROM serial and cannot be identified safely", value.Name),
			})
		case counts[value.USBSerial] != 1:
			result.Warnings = append(result.Warnings, Warning{
				Code: "serial_duplicate", USBSerial: value.USBSerial,
				Message: fmt.Sprintf("FTDI serial %q is present more than once and cannot be associated safely", value.USBSerial),
			})
		default:
			result.Ports = append(result.Ports, newPort(value))
		}
	}
	return result
}

func ValidateBaudRate(value int) error {
	if value < 300 || value > 4_000_000 {
		return fmt.Errorf("serial baud rate must be between 300 and 4000000")
	}
	return nil
}

func windowsFTDIIdentity(serialNumber string) (string, string, error) {
	if len(serialNumber) < 2 {
		return "", "", fmt.Errorf("FTDI channel serial is missing")
	}
	channel := strings.ToUpper(serialNumber[len(serialNumber)-1:])
	if channel != "A" && channel != "B" {
		return "", "", fmt.Errorf("FTDI channel suffix is missing")
	}
	return serialNumber[:len(serialNumber)-1], channel, nil
}
