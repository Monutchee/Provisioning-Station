// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package stationclient is the context-aware public HTTP client for a station.
// It never starts a daemon or accesses hardware directly.
package stationclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
	"github.com/Monutchee/Provisioning-Station/internal/tasks"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

const DefaultResponseBytes int64 = 8 << 20
const MaxTranscriptBytes = 1 << 20

type Config struct {
	URL              string
	Token            string
	HTTPClient       *http.Client
	MaxResponseBytes int64
	// LocalOnly restricts the URL to a literal loopback IP, bypassing proxies.
	LocalOnly bool
}

type Client struct {
	base       *url.URL
	token      string
	http       *http.Client
	importHTTP *http.Client
	max        int64
}

func New(config Config) (*Client, error) {
	u, err := url.Parse(config.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("station URL must be an http(s) origin without credentials, path, query, or fragment")
	}
	if strings.ContainsAny(config.Token, "\r\n") {
		return nil, errors.New("invalid API token")
	}
	if config.LocalOnly && !IsLoopbackURL(u) {
		return nil, errors.New("MCP station URL must use a loopback address")
	}
	hc := http.Client{Timeout: 30 * time.Second}
	if config.HTTPClient != nil {
		hc = *config.HTTPClient
	}
	if config.LocalOnly && hc.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		hc.Transport = transport
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	max := config.MaxResponseBytes
	if max <= 0 {
		max = DefaultResponseBytes
	}
	upload := hc
	if config.HTTPClient == nil {
		upload.Timeout = 10 * time.Minute
	}
	return &Client{base: u, token: config.Token, http: &hc, importHTTP: &upload, max: max}, nil
}

func IsLoopbackURL(u *url.URL) bool {
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// NewRequestID creates an idempotency key. Persist or retain it before Submit,
// and reuse exactly the same request when resolving an ambiguous network error.
func NewRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// APIError is a non-success HTTP response. Bodies are bounded and credentials
// are redacted before they are exposed to callers.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("station HTTP %d (%s): %s", e.Status, e.Code, e.Message)
}

type requestError struct {
	message string
	cause   error
}

func (e *requestError) Error() string { return "station request failed: " + e.message }
func (e *requestError) Unwrap() error { return e.cause }

// IsConnectionRefused distinguishes an absent listener from authentication,
// protocol, timeout, or other service failures. It never initiates a daemon.
func IsConnectionRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

func (c *Client) redact(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "[redacted]")
	}
	return strings.Map(func(r rune) rune {
		if r >= 32 && (r < 127 || r > 159) {
			return r
		}
		return -1
	}, s)
}
func (c *Client) request(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := *c.base
	u.Path = path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	hc := c.http
	if method == http.MethodPost && path == "/api/v1/artifacts" {
		hc = c.importHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &requestError{message: c.redact(err.Error()), cause: err}
	}
	return resp, nil
}
func (c *Client) responseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &e)
	if e.Error.Message == "" {
		e.Error.Message = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Code: c.redact(e.Error.Code), Message: c.redact(e.Error.Message)}
}
func (c *Client) json(ctx context.Context, method, path string, q url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.request(ctx, method, path, q, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.responseError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > c.max {
		return errors.New("station JSON response exceeds size limit")
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("invalid station JSON: %w", err)
	}
	return nil
}
func validID(id string, n int) error {
	if len(id) != n {
		return errors.New("invalid identifier")
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return errors.New("invalid identifier")
		}
	}
	return nil
}

type Capabilities struct {
	APIVersion string `json:"apiVersion"`
	Version    string `json:"version"`
	Artifact   struct {
		Schema               string `json:"schema"`
		FormatVersions       []int  `json:"formatVersions"`
		MaxCompressedBytes   int64  `json:"maxCompressedBytes"`
		MaxUncompressedBytes int64  `json:"maxUncompressedBytes"`
	} `json:"artifact"`
	DeploymentTasks tasks.Capabilities `json:"deploymentTasks"`
	Executors       []string           `json:"executors"`
	Operations      []string           `json:"operations"`
	TFTPListen      string             `json:"tftpListen"`
	Network         struct {
		IPv4Addresses         []string           `json:"ipv4Addresses"`
		IPv4Interfaces        []NetworkInterface `json:"ipv4Interfaces"`
		PreferredTFTPServerIP string             `json:"preferredTftpServerIp"`
	} `json:"network"`
	XSDB struct {
		Available bool   `json:"available"`
		Path      string `json:"path,omitempty"`
		Error     string `json:"error,omitempty"`
	} `json:"xsdb"`
	Serial struct {
		Available             bool   `json:"available"`
		DefaultBaudRate       int    `json:"defaultBaudRate"`
		MaxJobTranscriptBytes int64  `json:"maxJobTranscriptBytes"`
		ReplayBytes           int    `json:"replayBytes"`
		FTDIVendorID          string `json:"ftdiVendorId"`
		FTDIProductID         string `json:"ftdiProductId"`
		UARTChannel           string `json:"uartChannel"`
	} `json:"serial"`
	AuthRequired bool `json:"authRequired"`
}
type NetworkInterface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}
type Target struct {
	xsdb.Target
	SerialAssociation string              `json:"serialAssociation"`
	SerialPort        *serialconsole.Port `json:"serialPort,omitempty"`
}
type Submission struct {
	RequestID string   `json:"requestId"`
	JobID     string   `json:"jobId"`
	Job       jobs.Job `json:"job"`
}
type Transcript struct {
	Data         []byte `json:"data"`
	Offset       int64  `json:"offset"`
	NextOffset   int64  `json:"nextOffset"`
	CaptureState string `json:"captureState"`
	Truncated    bool   `json:"truncated"`
}

func (c *Client) Capabilities(ctx context.Context) (out Capabilities, err error) {
	err = c.json(ctx, "GET", "/api/v1/capabilities", nil, nil, &out)
	if err == nil && (out.APIVersion != "v1" || out.Artifact.Schema != artifact.SchemaName) {
		err = errors.New("endpoint is not a compatible Provisioning Station API")
	}
	return
}
func (c *Client) ListArtifacts(ctx context.Context) ([]artifact.StoredArtifact, error) {
	var out struct {
		Artifacts []artifact.StoredArtifact `json:"artifacts"`
	}
	err := c.json(ctx, "GET", "/api/v1/artifacts", nil, nil, &out)
	return out.Artifacts, err
}
func (c *Client) GetArtifact(ctx context.Context, id string) (out artifact.StoredArtifact, err error) {
	if err = validID(id, 64); err != nil {
		return
	}
	err = c.json(ctx, "GET", "/api/v1/artifacts/"+id, nil, nil, &out)
	return
}
func (c *Client) Targets(ctx context.Context, hwServerURL string) ([]Target, error) {
	var out struct {
		Targets []Target `json:"targets"`
	}
	err := c.json(ctx, "GET", "/api/v1/xilinx/targets", url.Values{"hwServerUrl": {hwServerURL}}, nil, &out)
	return out.Targets, err
}

// jobResponse defaults availability for servers predating this optional field.
// An explicit false from a retention-aware server remains false.
type jobResponse jobs.Job

func (job *jobResponse) UnmarshalJSON(data []byte) error {
	type plain jobResponse
	value := plain{ArtifactAvailable: true}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*job = jobResponse(value)
	return nil
}

func (c *Client) Storage(ctx context.Context) (out storagebudget.Status, err error) {
	err = c.json(ctx, "GET", "/api/v1/storage", nil, nil, &out)
	return
}

func (c *Client) CleanupStorage(ctx context.Context, apply bool) (out jobs.CleanupResult, err error) {
	err = c.json(ctx, "POST", "/api/v1/storage/cleanup", nil, struct {
		Apply bool `json:"apply"`
	}{apply}, &out)
	return
}

func (c *Client) ListJobs(ctx context.Context) ([]jobs.Job, error) {
	var out struct {
		Jobs []jobResponse `json:"jobs"`
	}
	err := c.json(ctx, "GET", "/api/v1/jobs", nil, nil, &out)
	result := make([]jobs.Job, len(out.Jobs))
	for index, job := range out.Jobs {
		result[index] = jobs.Job(job)
	}
	return result, err
}
func (c *Client) GetJob(ctx context.Context, id string) (out jobs.Job, err error) {
	if err = validID(id, 32); err != nil {
		return
	}
	out.ArtifactAvailable = true
	err = c.json(ctx, "GET", "/api/v1/jobs/"+id, nil, nil, &out)
	return
}
func (c *Client) Submit(ctx context.Context, request jobs.Request) (out jobs.Job, err error) {
	if err = validID(request.RequestID, 32); err != nil {
		return out, errors.New("Submit requires a retained 32-character lowercase hexadecimal requestId")
	}
	out.ArtifactAvailable = true
	err = c.json(ctx, "POST", "/api/v1/jobs", nil, request, &out)
	return
}
func (c *Client) Submission(ctx context.Context, id string) (out Submission, err error) {
	if err = validID(id, 32); err != nil {
		return
	}
	out.Job.ArtifactAvailable = true
	err = c.json(ctx, "GET", "/api/v1/submissions/"+id, nil, nil, &out)
	return
}
func (c *Client) Events(ctx context.Context, id string, after, limit int) ([]jobs.Event, error) {
	if err := validID(id, 32); err != nil {
		return nil, err
	}
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, errors.New("events require after >= 0 and limit 1..1000")
	}
	var out struct {
		Events []jobs.Event `json:"events"`
	}
	err := c.json(ctx, "GET", "/api/v1/jobs/"+id+"/events", url.Values{"after": {strconv.Itoa(after)}, "limit": {strconv.Itoa(limit)}}, nil, &out)
	return out.Events, err
}
func (c *Client) Cancel(ctx context.Context, id string) (out jobs.Job, err error) {
	if err = validID(id, 32); err != nil {
		return
	}
	out.ArtifactAvailable = true
	err = c.json(ctx, "POST", "/api/v1/jobs/"+id+"/cancel", nil, nil, &out)
	return
}
func (c *Client) SerialPorts(ctx context.Context) (out serialconsole.Discovery, err error) {
	err = c.json(ctx, "GET", "/api/v1/serial/ports", nil, nil, &out)
	return
}

// ImportArtifact streams one multipart archive. The station verifies the archive.
// The reader must return when read or closed; cancellation closes the pipe.
func (c *Client) ImportArtifact(ctx context.Context, source io.Reader) (out artifact.StoredArtifact, err error) {
	if source == nil {
		return out, errors.New("artifact reader required")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	defer reader.Close()
	go func() {
		part, e := multipartWriter.CreateFormFile("artifact", "artifact.mps")
		if e == nil {
			_, e = io.Copy(part, source)
		}
		if e == nil {
			e = multipartWriter.Close()
		}
		_ = writer.CloseWithError(e)
	}()
	resp, err := c.request(ctx, "POST", "/api/v1/artifacts", nil, reader, multipartWriter.FormDataContentType())
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, c.responseError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.max+1))
	if err != nil {
		return out, err
	}
	if int64(len(data)) > c.max {
		return out, errors.New("station JSON response exceeds size limit")
	}
	err = json.Unmarshal(data, &out)
	return
}

func (c *Client) Transcript(ctx context.Context, id string, offset int64, limit int) (out Transcript, err error) {
	if err = validID(id, 32); err != nil {
		return
	}
	if offset < 0 || limit < 1 || limit > MaxTranscriptBytes || offset > int64(^uint64(0)>>1)-int64(limit) {
		return out, errors.New("transcript requires nonnegative offset and limit 1..1048576")
	}
	u := *c.base
	u.Path = "/api/v1/jobs/" + id + "/serial-transcript"
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return out, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+int64(limit)-1))
	resp, err := c.http.Do(req)
	if err != nil {
		return out, &requestError{message: c.redact(err.Error()), cause: err}
	}
	defer resp.Body.Close()
	out.Offset = offset
	out.NextOffset = offset
	out.CaptureState = resp.Header.Get("X-MNC-Serial-Capture-State")
	out.Truncated = resp.Header.Get("X-MNC-Serial-Truncated") == "true"
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		var size int64
		if _, e := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes */%d", &size); e == nil && offset >= size {
			return out, nil
		}
	}
	if resp.StatusCode != http.StatusPartialContent && !(resp.StatusCode == http.StatusOK && offset == 0) {
		return out, c.responseError(resp)
	}
	if resp.StatusCode == http.StatusPartialContent {
		var start, end, total int64
		if _, e := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); e != nil || start != offset || end < start || end-start >= int64(limit) || total <= end {
			return out, errors.New("invalid transcript Content-Range")
		}
	}
	out.Data, err = io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return out, err
	}
	if len(out.Data) > limit {
		return out, errors.New("station transcript exceeds requested limit")
	}
	out.NextOffset = offset + int64(len(out.Data))
	return
}
