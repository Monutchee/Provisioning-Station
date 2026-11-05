// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
)

func TestStorageRoutesAuthenticationPreviewAndExpiry(t *testing.T) {
	root := t.TempDir()
	store, err := artifact.OpenStore(filepath.Join(root, "store"), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Import(context.Background(), bytes.NewReader(testArtifactArchive(t)))
	if err != nil {
		t.Fatal(err)
	}
	gate := storagebudget.New(root, 256<<20)
	store.SetBudget(gate)
	manager, err := jobs.OpenManager(filepath.Join(root, "jobs"), store, immediateRunner{}, jobs.WithStorageBudget(gate))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	server, err := New(Config{Version: "test", APIToken: "secret", XSDB: testResolver{}, Serial: testSerialService{}}, store, manager)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body []byte, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer secret")
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/storage"
		if method == http.MethodPost {
			path += "/cleanup"
		}
		if w := call(method, path, []byte(`{}`), false); w.Code != 401 {
			t.Fatalf("unprotected %s %d", path, w.Code)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "operator-note"), []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	w := call(http.MethodGet, "/api/v1/storage", nil, true)
	var status storagebudget.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.CompletedJobLimit != 30 || status.UnknownBytes < 7 || status.RecoveryLocations == nil {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if w := call(http.MethodPost, "/api/v1/storage/cleanup", []byte(`{"apply":false}`), true); w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	var original jobs.Request
	var first jobs.Job
	for i := range 31 {
		r := jobs.Request{RequestID: fmt.Sprintf("%032x", i+1), ArtifactID: a.ID}
		j, err := manager.Create(r)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original = r
			first = j
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := manager.Get(first.ID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	w = call(http.MethodGet, "/api/v1/submissions/"+original.RequestID, nil, true)
	if w.Code != 410 {
		t.Fatalf("expired get %d %s", w.Code, w.Body)
	}
	body, _ := json.Marshal(original)
	w = call(http.MethodPost, "/api/v1/jobs", body, true)
	if w.Code != 410 {
		t.Fatalf("expired replay %d %s", w.Code, w.Body)
	}
	original.TargetID = "different"
	body, _ = json.Marshal(original)
	w = call(http.MethodPost, "/api/v1/jobs", body, true)
	if w.Code != 409 {
		t.Fatalf("expired conflict %d %s", w.Code, w.Body)
	}
	jobsResponse := call(http.MethodGet, "/api/v1/jobs", nil, true)
	var list struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if jobsResponse.Code != 200 || json.Unmarshal(jobsResponse.Body.Bytes(), &list) != nil || len(list.Jobs) != 30 || !list.Jobs[0].ArtifactAvailable {
		t.Fatalf("inspection %d %s", jobsResponse.Code, jobsResponse.Body)
	}
	gate.Lock()
	s, _ := gate.StatusLocked()
	gate.Limit = s.UsedBytes + s.ReservedBytes
	gate.Unlock()
	body, _ = json.Marshal(jobs.Request{ArtifactID: a.ID})
	w = call(http.MethodPost, "/api/v1/jobs", body, true)
	if w.Code != 507 {
		t.Fatalf("full %d %s", w.Code, w.Body)
	}
}
