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
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
	"github.com/coder/websocket"
)

type testResolver struct {
	path        string
	err         error
	targets     []xsdb.Target
	discoverErr error
}

func (resolver testResolver) Resolve() (string, error) { return resolver.path, resolver.err }
func (resolver testResolver) Discover(context.Context, string) ([]xsdb.Target, error) {
	return resolver.targets, resolver.discoverErr
}

type immediateRunner struct{}

func (immediateRunner) Validate(artifact.StoredArtifact, jobs.Request) error { return nil }
func (immediateRunner) Run(_ context.Context, _ artifact.StoredArtifact, _ jobs.Request, emit func(string, string)) error {
	emit("info", "test runner completed")
	return nil
}

type testSerialService struct{ port serialconsole.Port }

func (service testSerialService) List(context.Context) (serialconsole.Discovery, error) {
	return serialconsole.Discovery{Ports: []serialconsole.Port{service.port}}, nil
}

func (service testSerialService) MatchCableSerial(_ context.Context, serialNumber string) (serialconsole.Port, string, error) {
	if service.port.USBSerial == serialNumber {
		return service.port, "matched", nil
	}
	return serialconsole.Port{}, "not_found", serialconsole.ErrPortNotFound
}

func (testSerialService) Attach(context.Context, serialconsole.AttachRequest) (*serialconsole.Attachment, error) {
	return nil, errors.New("serial attachment is not configured in this test")
}

func (testSerialService) DefaultBaudRate() int { return serialconsole.DefaultBaudRate }
func (testSerialService) ReplayLimit() int     { return serialconsole.DefaultReplayBytes }

type httpSerialDiscoverer struct{ port serialconsole.Port }

func (discoverer httpSerialDiscoverer) Discover(context.Context) (serialconsole.Discovery, error) {
	return serialconsole.Discovery{Ports: []serialconsole.Port{discoverer.port}}, nil
}

type httpSerialPort struct {
	reads  chan []byte
	closed chan struct{}
	once   sync.Once
	mutex  sync.Mutex
	writes []byte
}

func newHTTPSerialPort() *httpSerialPort {
	return &httpSerialPort{reads: make(chan []byte, 8), closed: make(chan struct{})}
}

func (port *httpSerialPort) Read(destination []byte) (int, error) {
	select {
	case data := <-port.reads:
		return copy(destination, data), nil
	case <-port.closed:
		return 0, io.EOF
	}
}

func (port *httpSerialPort) Write(data []byte) (int, error) {
	port.mutex.Lock()
	defer port.mutex.Unlock()
	port.writes = append(port.writes, data...)
	return len(data), nil
}

func (port *httpSerialPort) Close() error {
	port.once.Do(func() { close(port.closed) })
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
		XSDB: testResolver{path: "/opt/Xilinx/bin/xsdb", targets: []xsdb.Target{{
			ID: "3", Name: "PSU", DeviceIndex: "0", DeviceName: "xczu4ev",
			CableName: "Digilent USB Device", CableSerial: "210308A12345",
		}}},
		Serial: testSerialService{port: serialconsole.Port{
			ID: strings.Repeat("a", 64), Name: "/dev/ttyUSB1",
			VendorID: serialconsole.FTDIVendorID, ProductID: serialconsole.FT2232HProductID,
			USBSerial: "210308A12345", Channel: serialconsole.FT2232HUARTChannel,
		}},
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

func TestLoopbackClientDoesNotRequireBearerToken(t *testing.T) {
	services := newTestServices(t, "secret")
	for _, remoteAddress := range []string{"127.0.0.1:12345", "[::1]:12345"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
		request.RemoteAddr = remoteAddress
		if strings.HasPrefix(remoteAddress, "[") {
			request.Host = "[::1]:8042"
		} else {
			request.Host = "127.0.0.1:8042"
		}
		response := httptest.NewRecorder()
		services.handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"authRequired":false`) {
			t.Fatalf("remote %s: status=%d body=%s", remoteAddress, response.Code, response.Body)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	request.RemoteAddr = "172.0.0.1:12345"
	request.Host = "172.0.0.1:8042"
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("non-loopback status=%d body=%s", response.Code, response.Body)
	}
}

func TestDiscoverXilinxTargets(t *testing.T) {
	services := newTestServices(t, "")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/xilinx/targets?hwServerUrl=tcp%3A127.0.0.1%3A3121", nil)
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"cableSerial":"210308A12345"`) ||
		!strings.Contains(response.Body.String(), `"serialAssociation":"matched"`) ||
		!strings.Contains(response.Body.String(), `"name":"/dev/ttyUSB1"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestSerialSessionStreamsAndEnforcesControllerLease(t *testing.T) {
	store, err := artifact.OpenStore(filepath.Join(t.TempDir(), "store"), artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	jobManager, err := jobs.OpenManager(filepath.Join(t.TempDir(), "jobs"), store, immediateRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer jobManager.Close()
	port := serialconsole.Port{
		ID: "serial-port-a", Name: "/dev/ttyUSB1", VendorID: serialconsole.FTDIVendorID,
		ProductID: serialconsole.FT2232HProductID, USBSerial: "BOARD-A", Channel: serialconsole.FT2232HUARTChannel,
	}
	handle := newHTTPSerialPort()
	console, err := serialconsole.New(serialconsole.Config{
		Discoverer: httpSerialDiscoverer{port: port},
		Open:       func(string, int) (serialconsole.PortHandle, error) { return handle, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	apiServer, err := New(Config{XSDB: testResolver{path: "xsdb"}, Serial: console}, store, jobManager)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(apiServer.Handler())
	defer httpServer.Close()

	create := func(access string) (*http.Response, map[string]any) {
		t.Helper()
		body := fmt.Sprintf(`{"portId":%q,"baudRate":115200,"access":%q}`, port.ID, access)
		response, requestErr := http.Post(httpServer.URL+"/api/v1/serial/sessions", "application/json", strings.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var payload map[string]any
		if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
			if decodeErr := json.NewDecoder(response.Body).Decode(&payload); decodeErr != nil {
				response.Body.Close()
				t.Fatal(decodeErr)
			}
		}
		response.Body.Close()
		return response, payload
	}

	createdResponse, session := create("controller")
	if createdResponse.StatusCode != http.StatusCreated || createdResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create controller status = %d, body = %v", createdResponse.StatusCode, session)
	}
	conflictResponse, conflict := create("controller")
	if conflictResponse.StatusCode != http.StatusConflict {
		t.Fatalf("second controller status = %d, body = %v", conflictResponse.StatusCode, conflict)
	}
	observerResponse, observer := create("observer")
	if observerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create observer status = %d, body = %v", observerResponse.StatusCode, observer)
	}
	observerID := observer["id"].(string)
	deleteRequest, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/v1/serial/sessions/"+observerID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("delete observer status = %d", deleteResponse.StatusCode)
	}

	streamPath := session["websocketPath"].(string)
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + streamPath
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	badConnection, badResponse, badErr := websocket.Dial(ctx, websocketURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://malicious.example"}},
	})
	if badConnection != nil {
		badConnection.CloseNow()
	}
	if badResponse != nil && badResponse.Body != nil {
		badResponse.Body.Close()
	}
	if badErr == nil || badResponse == nil || badResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin dial error=%v response=%v", badErr, badResponse)
	}
	connection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	auth, _ := json.Marshal(map[string]string{"type": "attach", "token": session["attachToken"].(string)})
	if err := connection.Write(ctx, websocket.MessageText, auth); err != nil {
		t.Fatal(err)
	}
	messageType, ready, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || !bytes.Contains(ready, []byte(`"type":"ready"`)) {
		t.Fatalf("ready message = type %v, %s", messageType, ready)
	}
	if err := connection.Write(ctx, websocket.MessageBinary, []byte("help\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		handle.mutex.Lock()
		written := string(handle.writes)
		handle.mutex.Unlock()
		if written == "help\r" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serial writes = %q", written)
		}
		time.Sleep(time.Millisecond)
	}
	handle.reads <- []byte("booting\r\n")
	messageType, output, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary || string(output) != "booting\r\n" {
		t.Fatalf("serial output = type %v, %q", messageType, output)
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
		TargetID: "3", TargetCableSerial: "210308A12345", TargetDeviceIndex: "0",
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
	if job.Request.TargetID != "3" || job.Request.TargetCableSerial != "210308A12345" ||
		job.Request.TargetDeviceIndex != "0" {
		t.Fatalf("job target = %+v", job.Request)
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
