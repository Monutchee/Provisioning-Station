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
		return nameOnlyCandidates(err)
	}
	result := make([]candidate, 0)
	for _, port := range ports {
		if port == nil || port.Name == "" {
			continue
		}
		value := candidate{
			Name: port.Name, VendorID: strings.ToUpper(port.VID), ProductID: strings.ToUpper(port.PID),
			USBSerial: port.SerialNumber,
		}
		if port.IsUSB && strings.EqualFold(port.VID, FTDIVendorID) && strings.EqualFold(port.PID, FT2232HProductID) {
			if serialNumber, channel, identityErr := windowsFTDIIdentity(port.SerialNumber); identityErr == nil {
				value.USBSerial = serialNumber
				value.Channel = channel
			}
		}
		result = append(result, value)
	}
	return result, nil
}
