// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
)

type testResolver struct {
	path string
	err  error
}

func (resolver testResolver) Resolve() (string, error) { return resolver.path, resolver.err }

type immediateRunner struct{}

func (immediateRunner) Validate(artifact.StoredArtifact, jobs.Request) error { return nil }
func (immediateRunner) Run(_ context.Context, _ artifact.StoredArtifact, _ jobs.Request, emit func(string, string)) error {
	emit("info", "test runner completed")
	return nil
}

type testServices struct {
	handler http.Handler
	store   *artifact.Store
	jobs    *jobs.Manager
}

func newTestServices(t *testing.T, token string) testServices {
	t.Helper()
	store, err := artifact.OpenStore(filepath.Join(t.TempDir(), "store"), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.OpenManager(filepath.Join(t.TempDir(), "jobs"), store, immediateRunner{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	server, err := New(Config{
		Version: "test-version", APIToken: token, TFTPListen: ":69",
		XSDB: testResolver{path: "/opt/Xilinx/bin/xsdb"},
	}, store, manager)
	if err != nil {
		t.Fatal(err)
	}
	return testServices{handler: server.Handler(), store: store, jobs: manager}
}

func TestHealthAndEmbeddedUIArePublic(t *testing.T) {
	services := newTestServices(t, "secret")
	for _, path := range []string{"/api/v1/health", "/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		services.handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body)
		}
	}
}

func TestProtectedAPIRequiresExactBearerToken(t *testing.T) {
	services := newTestServices(t, "secret")
	for _, token := range []string{"", "wrong", "secret "} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		services.handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"test-version"`) {
		t.Fatalf("authorized capabilities: status=%d body=%s", response.Code, response.Body)
	}
}

func TestCapabilitiesPreferFirstDetectedInterfaceForTFTP(t *testing.T) {
	services := newTestServices(t, "")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var capabilities struct {
		Network struct {
			PreferredTFTPServerIP string   `json:"preferredTftpServerIp"`
			IPv4Addresses         []string `json:"ipv4Addresses"`
			IPv4Interfaces        []struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"ipv4Interfaces"`
		} `json:"network"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if len(capabilities.Network.IPv4Interfaces) == 0 {
		if capabilities.Network.PreferredTFTPServerIP != "" || len(capabilities.Network.IPv4Addresses) != 0 {
			t.Fatalf("network=%+v", capabilities.Network)
		}
		return
	}
	first := capabilities.Network.IPv4Interfaces[0]
	if first.Name == "" || capabilities.Network.PreferredTFTPServerIP != first.Address {
		t.Fatalf("network=%+v", capabilities.Network)
	}
	if len(capabilities.Network.IPv4Addresses) == 0 || capabilities.Network.IPv4Addresses[0] != first.Address {
		t.Fatalf("network=%+v", capabilities.Network)
	}
}

func TestArtifactUploadCreatesRunnableJobAndEvents(t *testing.T) {
	services := newTestServices(t, "")
	archive := testArtifactArchive(t)
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("artifact", "msap1-jtag-image.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", &requestBody)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body)
	}
	var stored artifact.StoredArtifact
	if err := json.Unmarshal(response.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}

	jobJSON, err := json.Marshal(jobs.Request{
		ArtifactID: stored.ID, HWServerURL: "tcp:127.0.0.1:3121", TFTPServerIP: "192.0.2.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(jobJSON))
	response = httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create job status=%d body=%s", response.Code, response.Body)
	}
	var job jobs.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, err := services.jobs.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == jobs.StateSucceeded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID+"/events", nil)
	response = httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "test runner completed") {
		t.Fatalf("events status=%d body=%s", response.Code, response.Body)
	}
}

func TestCreateJobRejectsUnknownJSONFields(t *testing.T) {
	services := newTestServices(t, "")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"artifactId":"x","unknown":true}`))
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestCreateJobRejectsOversizedJSON(t *testing.T) {
	services := newTestServices(t, "")
	body := `{"artifactId":"x"}` + strings.Repeat(" ", (64<<10)+1)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "exceeds") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func testArtifactArchive(t *testing.T) []byte {
	t.Helper()
	payload := map[string][]byte{
		"jtag/load-jtag-image.tcl": []byte("puts ok\n"),
		"tftp/Image":               []byte("kernel"),
	}
	files := make(map[string]artifact.FileDescriptor, len(payload))
	for name, data := range payload {
		digest := sha256.Sum256(data)
		mode := "0644"
		if strings.HasPrefix(name, "jtag/") {
			mode = "0755"
		}
		files[name] = artifact.FileDescriptor{
			Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: mode,
		}
	}
	manifest := artifact.Manifest{
		Schema: artifact.SchemaName, FormatVersion: artifact.FormatVersion,
		Artifact: artifact.ArtifactMetadata{
			Name: "msap1-jtag-image", Vendor: "xilinx", Operation: "jtag-boot",
			Product: "msap1", Machine: "msap1", Version: "1.0", BuildID: "test",
			CreatedUTC: "2026-08-30T00:00:00Z",
		},
		Executor: artifact.ExecutorMetadata{
			Type: "xilinx-xsdb", Entrypoint: "jtag/load-jtag-image.tcl", TFTPRoot: "tftp",
		},
		Files: files,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range append([]string{artifact.ManifestName}, artifact.SortedPayloadPaths(manifest)...) {
		data := payload[name]
		mode := int64(0o644)
		if name == artifact.ManifestName {
			data = manifestJSON
		} else if manifest.Files[name].Mode == "0755" {
			mode = 0o755
		}
		header := &tar.Header{
			Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg,
			Uid: 0, Gid: 0, Format: tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
