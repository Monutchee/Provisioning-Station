// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package serialconsole

import "testing"

func TestNormalizeCandidatesRequiresUniqueSerial(t *testing.T) {
	result := normalizeCandidates([]candidate{
		{Name: "/dev/ttyUSB1", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-A", Channel: "B"},
		{Name: "/dev/ttyUSB2", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-B", Channel: "B"},
		{Name: "/dev/ttyUSB3", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-B", Channel: "B"},
		{Name: "/dev/ttyUSB4", VendorID: FTDIVendorID, ProductID: FT2232HProductID, Channel: "B"},
	})
	if len(result.Ports) != 4 {
		t.Fatalf("ports = %+v", result.Ports)
	}
	if len(result.Warnings) != 3 {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	ids := make(map[string]struct{})
	for _, port := range result.Ports {
		if port.ID == "" {
			t.Fatalf("port has no ID: %+v", port)
		}
		ids[port.ID] = struct{}{}
	}
	if len(ids) != len(result.Ports) {
		t.Fatalf("manual fallback IDs are not unique: %+v", result.Ports)
	}
	matched := newPort(candidate{
		Name: "/dev/ttyUSB1", VendorID: FTDIVendorID, ProductID: FT2232HProductID,
		USBSerial: "BOARD-A", Channel: "B",
	})
	if result.Ports[0].ID != matched.ID {
		t.Fatalf("safe automatic ID changed: got %s want %s", result.Ports[0].ID, matched.ID)
	}
}

func TestNormalizeCandidatesListsGenericAndUnidentifiedPortsForManualSelection(t *testing.T) {
	result := normalizeCandidates([]candidate{
		{Name: "COM3"},
		{Name: "COM7", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-A"},
	})
	if len(result.Ports) != 2 || result.Ports[0].Name != "COM3" || result.Ports[1].Name != "COM7" {
		t.Fatalf("ports = %+v", result.Ports)
	}
	if result.Ports[0].ID == result.Ports[1].ID || len(result.Warnings) != 0 {
		t.Fatalf("discovery = %+v", result)
	}
}

func TestStablePortIDIgnoresOperatingSystemName(t *testing.T) {
	first := newPort(candidate{Name: "/dev/ttyUSB7", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-A", Channel: "B"})
	second := newPort(candidate{Name: "COM42", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-A", Channel: "B"})
	if first.ID != second.ID {
		t.Fatalf("IDs differ: %s != %s", first.ID, second.ID)
	}
}

func TestValidateBaudRate(t *testing.T) {
	for _, value := range []int{300, 115200, 4_000_000} {
		if err := ValidateBaudRate(value); err != nil {
			t.Errorf("ValidateBaudRate(%d) = %v", value, err)
		}
	}
	for _, value := range []int{0, 299, 4_000_001} {
		if err := ValidateBaudRate(value); err == nil {
			t.Errorf("ValidateBaudRate(%d) succeeded", value)
		}
	}
}

func TestWindowsFTDIIdentitySeparatesInterfaceSuffix(t *testing.T) {
	serialNumber, channel, err := windowsFTDIIdentity("BOARD-123b")
	if err != nil {
		t.Fatal(err)
	}
	if serialNumber != "BOARD-123" || channel != "B" {
		t.Fatalf("identity = %q channel %q", serialNumber, channel)
	}
	for _, value := range []string{"", "B", "BOARD-123"} {
		if _, _, err := windowsFTDIIdentity(value); err == nil {
			t.Errorf("windowsFTDIIdentity(%q) succeeded", value)
		}
	}
}
