// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/webui"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

const APIVersion = "v1"

type Resolver interface {
	Resolve() (string, error)
	Discover(context.Context, string) ([]xsdb.Target, error)
}

type SerialService interface {
	List(context.Context) (serialconsole.Discovery, error)
	MatchCableSerial(context.Context, string) (serialconsole.Port, string, error)
	Attach(context.Context, serialconsole.AttachRequest) (*serialconsole.Attachment, error)
	DefaultBaudRate() int
	ReplayLimit() int
}

type Config struct {
	Version            string
	APIToken           string
	TFTPListen         string
	XSDB               Resolver
	Serial             SerialService
	MaxBodyBytes       int64
	MaxConsoleLogBytes int64
}

type Server struct {
	config    Config
	artifacts *artifact.Store
	jobs      *jobs.Manager
	serial    SerialService
	sessions  *serialSessions
	handler   http.Handler
}

func New(config Config, artifacts *artifact.Store, jobManager *jobs.Manager) (*Server, error) {
	if artifacts == nil || jobManager == nil || config.XSDB == nil || config.Serial == nil {
		return nil, fmt.Errorf("HTTP API requires artifact, job, XSDB, and serial-console services")
	}
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = artifacts.Limits().MaxCompressedBytes + (1 << 20)
	}
	if config.MaxConsoleLogBytes <= 0 {
		config.MaxConsoleLogBytes = serialconsole.DefaultLogBytes
	}
	server := &Server{
		config: config, artifacts: artifacts, jobs: jobManager, serial: config.Serial,
		sessions: newSerialSessions(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", server.health)
	mux.Handle("GET /api/v1/capabilities", server.authorize(http.HandlerFunc(server.capabilities)))
	mux.Handle("GET /api/v1/artifacts", server.authorize(http.HandlerFunc(server.listArtifacts)))
	mux.Handle("POST /api/v1/artifacts", server.authorize(http.HandlerFunc(server.importArtifact)))
	mux.Handle("GET /api/v1/artifacts/{id}", server.authorize(http.HandlerFunc(server.getArtifact)))
	mux.Handle("GET /api/v1/xilinx/targets", server.authorize(http.HandlerFunc(server.xilinxTargets)))
	mux.Handle("GET /api/v1/serial/ports", server.authorize(http.HandlerFunc(server.serialPorts)))
	mux.Handle("POST /api/v1/serial/sessions", server.authorize(http.HandlerFunc(server.createSerialSession)))
	mux.Handle("DELETE /api/v1/serial/sessions/{id}", server.authorize(http.HandlerFunc(server.deleteSerialSession)))
	mux.HandleFunc("GET /api/v1/serial/sessions/{id}/stream", server.streamSerialSession)
	mux.Handle("GET /api/v1/jobs", server.authorize(http.HandlerFunc(server.listJobs)))
	mux.Handle("POST /api/v1/jobs", server.authorize(http.HandlerFunc(server.createJob)))
	mux.Handle("GET /api/v1/jobs/{id}", server.authorize(http.HandlerFunc(server.getJob)))
	mux.Handle("POST /api/v1/jobs/{id}/cancel", server.authorize(http.HandlerFunc(server.cancelJob)))
	mux.Handle("GET /api/v1/jobs/{id}/events", server.authorize(http.HandlerFunc(server.jobEvents)))
	mux.Handle("GET /api/v1/jobs/{id}/serial-transcript", server.authorize(http.HandlerFunc(server.jobSerialTranscript)))
	mux.Handle("/", webui.Handler())
	server.handler = securityHeaders(mux)
	return server, nil
}

func (server *Server) Handler() http.Handler {
	return server.handler
}

func (server *Server) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    server.config.Version,
		"apiVersion": APIVersion,
		"time":       time.Now().UTC(),
	})
}

func (server *Server) capabilities(response http.ResponseWriter, request *http.Request) {
	xsdbPath, xsdbErr := server.config.XSDB.Resolve()
	xsdbStatus := map[string]any{"available": xsdbErr == nil}
	if xsdbErr == nil {
		xsdbStatus["path"] = xsdbPath
	} else {
		xsdbStatus["error"] = xsdbErr.Error()
	}
	limits := server.artifacts.Limits()
	stationInterfaces := stationIPv4Interfaces()
	stationAddresses := make([]string, 0, len(stationInterfaces))
	for _, networkInterface := range stationInterfaces {
		stationAddresses = append(stationAddresses, networkInterface.Address)
	}
	networkStatus := map[string]any{
		"ipv4Addresses":  stationAddresses,
		"ipv4Interfaces": stationInterfaces,
	}
	if len(stationInterfaces) != 0 {
		networkStatus["preferredTftpServerIp"] = stationInterfaces[0].Address
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"apiVersion": APIVersion,
		"version":    server.config.Version,
		"artifact": map[string]any{
			"schema":               artifact.SchemaName,
			"formatVersions":       []int{artifact.FormatVersion},
			"maxCompressedBytes":   limits.MaxCompressedBytes,
			"maxUncompressedBytes": limits.MaxUncompressedBytes,
		},
		"executors":  []string{"xilinx-xsdb"},
		"operations": []string{"jtag-boot"},
		"tftpListen": server.config.TFTPListen,
		"network":    networkStatus,
		"xsdb":       xsdbStatus,
		"serial": map[string]any{
			"available":             true,
			"defaultBaudRate":       server.serial.DefaultBaudRate(),
			"maxJobTranscriptBytes": server.config.MaxConsoleLogBytes,
			"replayBytes":           server.serial.ReplayLimit(),
			"ftdiVendorId":          serialconsole.FTDIVendorID,
			"ftdiProductId":         serialconsole.FT2232HProductID,
			"uartChannel":           serialconsole.FT2232HUARTChannel,
		},
		"authRequired": server.config.APIToken != "" && !isDirectLoopbackRequest(request),
	})
}

func (server *Server) xilinxTargets(response http.ResponseWriter, request *http.Request) {
	hardwareServerURL := request.URL.Query().Get("hwServerUrl")
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	targets, err := server.config.XSDB.Discover(ctx, hardwareServerURL)
	if err != nil {
		writeError(response, http.StatusBadRequest, "target_discovery_failed", err.Error())
		return
	}
	associated := make([]xilinxTargetResponse, 0, len(targets))
	for _, target := range targets {
		item := xilinxTargetResponse{Target: target, SerialAssociation: "not_found"}
		if target.CableSerial != "" {
			port, status, matchErr := server.serial.MatchCableSerial(ctx, target.CableSerial)
			item.SerialAssociation = status
			if matchErr == nil {
				item.SerialPort = &port
			}
		}
		associated = append(associated, item)
	}
	writeJSON(response, http.StatusOK, map[string]any{"targets": associated})
}

type xilinxTargetResponse struct {
	xsdb.Target
	SerialAssociation string              `json:"serialAssociation"`
	SerialPort        *serialconsole.Port `json:"serialPort,omitempty"`
}

func (server *Server) listArtifacts(response http.ResponseWriter, _ *http.Request) {
	artifacts, err := server.artifacts.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "artifact_list_failed", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"artifacts": artifacts})
}

func (server *Server) importArtifact(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, server.config.MaxBodyBytes)
	multipartReader, err := request.MultipartReader()
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_multipart", "request must be multipart/form-data")
		return
	}
	var imported *artifact.StoredArtifact
	for {
		part, err := multipartReader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_multipart", err.Error())
			return
		}
		if part.FormName() != "artifact" || part.FileName() == "" {
			part.Close()
			writeError(response, http.StatusBadRequest, "unexpected_form_field", "the only accepted form field is the artifact file")
			return
		}
		if imported != nil {
			part.Close()
			writeError(response, http.StatusBadRequest, "multiple_artifacts", "upload exactly one artifact")
			return
		}
		stored, err := server.artifacts.Import(request.Context(), part)
		part.Close()
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "compressed size limit") || strings.Contains(err.Error(), "request body too large") {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(response, status, "invalid_artifact", err.Error())
			return
		}
		imported = &stored
	}
	if imported == nil {
		writeError(response, http.StatusBadRequest, "artifact_missing", "multipart field 'artifact' is required")
		return
	}
	writeJSON(response, http.StatusCreated, imported)
}

func (server *Server) getArtifact(response http.ResponseWriter, request *http.Request) {
	stored, err := server.artifacts.Load(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, "artifact_not_found", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, stored)
}

func (server *Server) listJobs(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{"jobs": server.jobs.List()})
}

func (server *Server) createJob(response http.ResponseWriter, request *http.Request) {
	var jobRequest jobs.Request
	if err := decodeJSON(request, &jobRequest); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := server.jobs.CreateContext(request.Context(), jobRequest)
	if err != nil {
		writeError(response, http.StatusBadRequest, "job_rejected", err.Error())
		return
	}
	response.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	writeJSON(response, http.StatusCreated, job)
}

func (server *Server) getJob(response http.ResponseWriter, request *http.Request) {
	job, err := server.jobs.Get(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, "job_not_found", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, job)
}

func (server *Server) cancelJob(response http.ResponseWriter, request *http.Request) {
	job, err := server.jobs.Cancel(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, "job_not_found", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, job)
}

func (server *Server) jobEvents(response http.ResponseWriter, request *http.Request) {
	after, err := parseAfter(request.URL.Query().Get("after"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_after", err.Error())
		return
	}
	if !strings.Contains(request.Header.Get("Accept"), "text/event-stream") {
		events, err := server.jobs.Events(request.PathValue("id"), after)
		if err != nil {
			writeError(response, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"events": events})
		return
	}
	server.streamEvents(response, request, after)
}

func (server *Server) streamEvents(response http.ResponseWriter, request *http.Request, after int) {
	history, channel, unsubscribe, err := server.jobs.Subscribe(request.PathValue("id"), after)
	if err != nil {
		writeError(response, http.StatusNotFound, "job_not_found", err.Error())
		return
	}
	defer unsubscribe()
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusInternalServerError, "streaming_unavailable", "HTTP streaming is unavailable")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(event jobs.Event) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(response, "id: %d\nevent: log\ndata: %s\n\n", event.Sequence, data)
		if err == nil {
			flusher.Flush()
		}
		return err == nil
	}
	for _, event := range history {
		if !writeEvent(event) {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-channel:
			if !open {
				_, _ = io.WriteString(response, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			if !writeEvent(event) {
				return
			}
		case <-ticker.C:
			_, _ = io.WriteString(response, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (server *Server) authorize(next http.Handler) http.Handler {
	if server.config.APIToken == "" {
		return next
	}
	expected := []byte(server.config.APIToken)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if isDirectLoopbackRequest(request) {
			next.ServeHTTP(response, request)
			return
		}
		value := request.Header.Get("Authorization")
		provided := []byte(strings.TrimPrefix(value, "Bearer "))
		if !strings.HasPrefix(value, "Bearer ") || len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			response.Header().Set("WWW-Authenticate", "Bearer")
			writeError(response, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func isDirectLoopbackRequest(request *http.Request) bool {
	remoteHost, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		remoteHost = request.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.Trim(remoteHost, "[]"))
	if remoteIP == nil || !remoteIP.IsLoopback() {
		return false
	}

	requestHost, _, err := net.SplitHostPort(request.Host)
	if err != nil {
		requestHost = request.Host
	}
	requestIP := net.ParseIP(strings.Trim(requestHost, "[]"))
	return requestIP != nil && requestIP.IsLoopback()
}

func decodeJSON(request *http.Request, destination any) error {
	const maximumJSONBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(request.Body, maximumJSONBytes+1))
	if err != nil {
		return fmt.Errorf("read JSON request: %w", err)
	}
	if len(data) > maximumJSONBytes {
		return fmt.Errorf("JSON request exceeds %d bytes", maximumJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON request contains trailing data")
	}
	return nil
}

func parseAfter(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	after, err := strconv.Atoi(value)
	if err != nil || after < 0 {
		return 0, fmt.Errorf("after must be a non-negative event sequence")
	}
	return after, nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(value)
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(response, request)
	})
}
