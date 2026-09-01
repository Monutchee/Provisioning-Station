// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package serialconsole

import (
	"context"
	"fmt"
	"sort"
)

type Discoverer interface {
	Discover(context.Context) (Discovery, error)
}

type NativeDiscoverer struct{}

func (NativeDiscoverer) Discover(ctx context.Context) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	candidates, err := nativeCandidates()
	if err != nil {
		return Discovery{}, fmt.Errorf("discover serial consoles: %w", err)
	}
	result := normalizeCandidates(candidates)
	sort.Slice(result.Ports, func(first, second int) bool {
		if result.Ports[first].USBSerial != result.Ports[second].USBSerial {
			return result.Ports[first].USBSerial < result.Ports[second].USBSerial
		}
		return result.Ports[first].Name < result.Ports[second].Name
	})
	return result, nil
}
