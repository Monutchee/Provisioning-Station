// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package serialconsole

import (
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
		serialNumber, channel, err := windowsFTDIIdentity(port.SerialNumber)
		if err != nil || channel != FT2232HUARTChannel {
			continue
		}
		result = append(result, candidate{
			Name: port.Name, VendorID: FTDIVendorID, ProductID: FT2232HProductID,
			USBSerial: serialNumber, Channel: channel,
		})
	}
	return result, nil
}
