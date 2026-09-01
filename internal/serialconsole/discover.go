// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package serialconsole

import (
	"context"
	"fmt"
	"sort"

	"go.bug.st/serial"
)

type Discoverer interface {
	Discover(context.Context) (Discovery, error)
}

type NativeDiscoverer struct{}

func nameOnlyCandidates(detailErr error) ([]candidate, error) {
	names, err := serial.GetPortsList()
	if err != nil {
		return nil, fmt.Errorf("discover serial details: %v; list serial ports: %w", detailErr, err)
	}
	result := make([]candidate, 0, len(names))
	for _, name := range names {
		if name != "" {
			result = append(result, candidate{Name: name})
		}
	}
	return result, nil
}

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
		if result.Ports[first].Name != result.Ports[second].Name {
			return result.Ports[first].Name < result.Ports[second].Name
		}
		return result.Ports[first].ID < result.Ports[second].ID
	})
	return result, nil
}
