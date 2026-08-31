// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"net"
)

type stationIPv4Interface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

func stationIPv4Interfaces() []stationIPv4Interface {
	result := make([]stationIPv4Interface, 0, 4)
	seen := make(map[string]struct{})
	appendIP := func(name string, ip net.IP) {
		ipv4 := ip.To4()
		if ipv4 == nil || ipv4.IsUnspecified() || ipv4.IsLoopback() || !ipv4.IsGlobalUnicast() {
			return
		}
		value := ipv4.String()
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		result = append(result, stationIPv4Interface{Name: name, Address: value})
	}

	interfaces, err := net.Interfaces()
	if err == nil {
		for _, networkInterface := range interfaces {
			if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
				continue
			}
			assigned, addressErr := networkInterface.Addrs()
			if addressErr != nil {
				continue
			}
			for _, address := range assigned {
				var ip net.IP
				switch value := address.(type) {
				case *net.IPNet:
					ip = value.IP
				case *net.IPAddr:
					ip = value.IP
				}
				appendIP(networkInterface.Name, ip)
			}
		}
	}
	return result
}
