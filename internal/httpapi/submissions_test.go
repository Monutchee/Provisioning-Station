// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Monutchee/Provisioning-Station/internal/jobs"
)

func TestSubmissionReconciliationAndBoundedEvents(t *testing.T) {
	services := newTestServices(t, "secret")
	stored, err := services.store.Import(context.Background(), bytes.NewReader(testArtifactArchive(t)))
	if err != nil {
		t.Fatal(err)
	}
	selection := jobs.Request{RequestID: strings.Repeat("a", 32), ArtifactID: stored.ID, HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10"}
	call := func(method, path string, body []byte, token bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token {
			request.Header.Set("Authorization", "Bearer secret")
		}
		response := httptest.NewRecorder()
		services.handler.ServeHTTP(response, request)
		return response
	}
	data, _ := json.Marshal(selection)
	first := call(http.MethodPost, "/api/v1/jobs", data, true)
	if first.Code != 201 {
		t.Fatalf("first %d %s", first.Code, first.Body)
	}
	var original jobs.Job
	if err := json.Unmarshal(first.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	// Treat the first response as lost: reconciliation and same-ID retry must find it.
	path := "/api/v1/submissions/" + selection.RequestID
	if response := call(http.MethodGet, path, nil, false); response.Code != 401 {
		t.Fatalf("unprotected reconciliation: %d", response.Code)
	}
	response := call(http.MethodGet, path, nil, true)
	var submission struct {
		JobID string `json:"jobId"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &submission) != nil || submission.JobID != original.ID {
		t.Fatalf("reconcile %d %s", response.Code, response.Body)
	}
	response = call(http.MethodPost, "/api/v1/jobs", data, true)
	if response.Code != 200 {
		t.Fatalf("retry %d %s", response.Code, response.Body)
	}
	selection.TargetID = "2"
	data, _ = json.Marshal(selection)
	if response := call(http.MethodPost, "/api/v1/jobs", data, true); response.Code != 409 {
		t.Fatalf("conflict %d %s", response.Code, response.Body)
	}
	if response := call(http.MethodGet, "/api/v1/submissions/"+strings.Repeat("b", 32), nil, true); response.Code != 404 {
		t.Fatalf("unknown %d", response.Code)
	}
	eventsPath := "/api/v1/jobs/" + original.ID + "/events"
	response = call(http.MethodGet, eventsPath+"?limit=1", nil, true)
	var page struct {
		Events []jobs.Event `json:"events"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Events) != 1 || page.Events[0].Sequence != 1 {
		t.Fatalf("page %d %s", response.Code, response.Body)
	}
	for _, limit := range []string{"0", "-1", "1001", "bad", "", "1&limit=2"} {
		if response := call(http.MethodGet, eventsPath+"?limit="+limit, nil, true); response.Code != 400 {
			t.Fatalf("limit %q: %d", limit, response.Code)
		}
	}
	response = call(http.MethodGet, eventsPath+"?after=1&limit=1", nil, true)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Events) > 1 || (len(page.Events) > 0 && page.Events[0].Sequence != 2) {
		t.Fatalf("next page %d %s", response.Code, response.Body)
	}
}
