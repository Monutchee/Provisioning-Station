// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import (
	"context"
	"errors"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
)

func TestReadinessStartsOnlyRefusedDefaultListener(t *testing.T) {
	for _, test := range []struct {
		name, url string
		status    int
		refused   bool
		want      int
	}{{"absent", DefaultURL, 0, true, 1}, {"custom", "http://127.0.0.1:8043", 0, true, 0}, {"auth", DefaultURL, 401, false, 0}, {"unrelated", DefaultURL, 200, false, 0}} {
		t.Run(test.name, func(t *testing.T) {
			started := 0
			c := clientFor(t, func(*http.Request) (*http.Response, error) {
				if started > 0 {
					return response(200, `{"apiVersion":"v1","artifact":{"schema":"`+artifact.SchemaName+`"}}`), nil
				}
				if test.refused {
					return nil, syscall.ECONNREFUSED
				}
				return response(test.status, `{}`), nil
			})
			err := EnsureReady(context.Background(), c, ReadinessConfig{URL: test.url, Start: func(context.Context) error { started++; return nil }})
			if started != test.want {
				t.Fatalf("started %d", started)
			}
			if test.want == 1 && err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestReadinessCancellationAndNoEscalation(t *testing.T) {
	c := clientFor(t, func(*http.Request) (*http.Response, error) { return nil, syscall.ECONNREFUSED })
	denied := errors.New("service permission denied")
	if err := EnsureReady(context.Background(), c, ReadinessConfig{URL: DefaultURL, Start: func(context.Context) error { return denied }}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err := EnsureReady(context.Background(), c, ReadinessConfig{URL: DefaultURL, Timeout: time.Millisecond, PollInterval: time.Millisecond, Start: func(context.Context) error { return nil }}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
