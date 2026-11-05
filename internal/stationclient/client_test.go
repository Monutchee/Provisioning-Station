// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package stationclient

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func clientFor(t *testing.T, f roundTripFunc) *Client {
	t.Helper()
	c, e := New(Config{URL: "http://127.0.0.1:9999", Token: "secret-test-token", HTTPClient: &http.Client{Transport: f}})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestConfigurationAndRedirectSafety(t *testing.T) {
	for _, u := range []string{"file:///tmp/x", "http://user:secret@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?q=x", "http://127.0.0.1#x", "http://example.com", "http://localhost"} {
		if _, e := New(Config{URL: u, LocalOnly: true}); e == nil {
			t.Errorf("accepted %s", u)
		}
	}
	c, e := New(Config{URL: "http://127.0.0.1", LocalOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("local client uses environment proxy")
	}
	count := 0
	c = clientFor(t, func(r *http.Request) (*http.Response, error) {
		count++
		if r.Header.Get("Authorization") != "Bearer secret-test-token" {
			t.Error("missing auth")
		}
		resp := response(302, "")
		resp.Header.Set("Location", "http://example.com/stolen")
		return resp, nil
	})
	if _, e = c.ListJobs(context.Background()); e == nil || count != 1 {
		t.Fatalf("redirect followed: calls %d err %v", count, e)
	}
}
func TestBoundsValidationAndRedaction(t *testing.T) {
	c := clientFor(t, func(*http.Request) (*http.Response, error) { return response(200, strings.Repeat("x", 100)), nil })
	c.max = 16
	if _, e := c.ListJobs(context.Background()); e == nil || !strings.Contains(e.Error(), "size limit") {
		t.Fatal(e)
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) {
		return response(401, `{"error":{"code":"secret-test-token","message":"bad secret-test-token"}}`), nil
	})
	if _, e := c.ListJobs(context.Background()); e == nil || strings.Contains(e.Error(), "secret-test-token") {
		t.Fatal(e)
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) { return response(200, `{}`), nil })
	if _, e := c.Capabilities(context.Background()); e == nil {
		t.Fatal("unrelated service accepted")
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"apiVersion":"v1","artifact":{"schema":"`+artifact.SchemaName+`"}}`), nil
	})
	if _, e := c.Capabilities(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestCancellationAndConnectionRefusalPreserveIdentity(t *testing.T) {
	c := clientFor(t, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.ListJobs(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("lost cancellation: %v", e)
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) { return nil, syscall.ECONNREFUSED })
	if _, e := c.ListJobs(context.Background()); !IsConnectionRefused(e) {
		t.Fatalf("lost connection refusal: %v", e)
	}
}
func TestSubmissionRetainsKeyAndBoundedCursor(t *testing.T) {
	key := strings.Repeat("a", 32)
	calls := 0
	c := clientFor(t, func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/api/v1/jobs":
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), `"requestId":"`+key+`"`) {
				t.Error("request key missing")
			}
			return nil, syscall.ECONNREFUSED
		case "/api/v1/jobs/" + key + "/events":
			if r.URL.Query().Get("after") != "7" || r.URL.Query().Get("limit") != "3" {
				t.Error("cursor missing")
			}
			return response(200, `{"events":[{"sequence":8}]}`), nil
		}
		t.Fatal(r.URL)
		return nil, nil
	})
	req := jobs.Request{RequestID: key}
	for range 2 {
		_, _ = c.Submit(context.Background(), req)
	}
	if req.RequestID != key {
		t.Fatal("key mutated")
	}
	events, e := c.Events(context.Background(), key, 7, 3)
	if e != nil || len(events) != 1 || events[0].Sequence != 8 {
		t.Fatal(events, e)
	}
	if _, e = c.Submit(context.Background(), jobs.Request{}); e == nil {
		t.Fatal("missing key allowed")
	}
	if _, e = c.Events(context.Background(), key, 0, 1001); e == nil {
		t.Fatal("unbounded events allowed")
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}
func TestStreamingMultipartAndTranscript(t *testing.T) {
	key := strings.Repeat("a", 32)
	c := clientFor(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			_, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if e != nil {
				t.Fatal(e)
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			part, e := reader.NextPart()
			if e != nil {
				t.Fatal(e)
			}
			data, _ := io.ReadAll(part)
			if part.FormName() != "artifact" || string(data) != "archive bytes" {
				t.Fatal(part.FormName(), string(data))
			}
			if _, e = reader.NextPart(); e != io.EOF {
				t.Fatal(e)
			}
			return response(201, `{"id":"ok"}`), nil
		}
		if r.Header.Get("Range") != "bytes=4-6" {
			t.Fatal(r.Header)
		}
		resp := response(206, "abc")
		resp.Header.Set("Content-Range", "bytes 4-6/9")
		return resp, nil
	})
	if out, e := c.ImportArtifact(context.Background(), strings.NewReader("archive bytes")); e != nil || out.ID != "ok" {
		t.Fatal(out, e)
	}
	if out, e := c.Transcript(context.Background(), key, 4, 3); e != nil || string(out.Data) != "abc" || out.NextOffset != 7 {
		t.Fatal(out, e)
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) { return response(200, "ignored range"), nil })
	if _, e := c.Transcript(context.Background(), key, 4, 3); e == nil {
		t.Fatal("ignored range accepted")
	}
	c = clientFor(t, func(*http.Request) (*http.Response, error) {
		resp := response(416, "")
		resp.Header.Set("Content-Range", "bytes */4")
		return resp, nil
	})
	if out, e := c.Transcript(context.Background(), key, 4, 3); e != nil || out.NextOffset != 4 {
		t.Fatal(out, e)
	}
}
