// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package mcpserver exposes the authenticated local station HTTP API over MCP.
// Request cancellation ends observation; only cancel_job cancels a durable job.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	Station stationclient.Config
	Version string
	// ImportRoot is host configuration, never a model-supplied absolute path.
	// Empty disables the import tool entirely.
	ImportRoot     string
	MaxImportBytes int64
}

type Server struct {
	*mcp.Server
	client    *stationclient.Client
	root      *os.Root
	maxImport int64
}

func New(config Config) (*Server, error) {
	config.Station.LocalOnly = true
	client, err := stationclient.New(config.Station)
	if err != nil {
		return nil, err
	}
	var root *os.Root
	if config.ImportRoot != "" {
		if !filepath.IsAbs(config.ImportRoot) {
			return nil, errors.New("MCP import root must be absolute")
		}
		root, err = os.OpenRoot(config.ImportRoot)
		if err != nil {
			return nil, errors.New("cannot open configured MCP import root")
		}
	}
	max := config.MaxImportBytes
	if max <= 0 {
		max = artifact.DefaultLimits().MaxCompressedBytes
	}
	version := config.Version
	if version == "" {
		version = "dev"
	}
	server := &Server{Server: mcp.NewServer(&mcp.Implementation{Name: "mps", Version: version}, &mcp.ServerOptions{Instructions: "Use the existing local station. Never start a daemon. Discover a target and obtain explicit user reset authorization before submit_job. Retain requestId and the exact request before submission; reconcile ambiguous errors with get_submission. Disconnecting or canceling an MCP request does not cancel a durable job; use cancel_job explicitly. Event and transcript reads use bounded cursors."}), client: client, root: root, maxImport: max}
	server.register()
	return server, nil
}
func (s *Server) Close() error {
	if s.root != nil {
		return s.root.Close()
	}
	return nil
}
func (s *Server) RunStdio(ctx context.Context) error { return s.Run(ctx, &mcp.StdioTransport{}) }

type emptyInput struct{}
type artifactInput struct {
	ArtifactID string `json:"artifactId"`
}
type jobInput struct {
	JobID string `json:"jobId"`
}
type submissionInput struct {
	RequestID string `json:"requestId"`
}
type targetsInput struct {
	HWServerURL string `json:"hwServerUrl,omitempty"`
}
type importInput struct {
	Path string `json:"path" jsonschema:"Relative path beneath the host-configured import root"`
}
type submitInput struct {
	Request          jobs.Request `json:"request"`
	AcknowledgeReset bool         `json:"acknowledgeReset" jsonschema:"True only after explicit user authorization to reset this selected physical target"`
}
type eventsInput struct {
	JobID string `json:"jobId"`
	After int    `json:"after" jsonschema:"Last received event sequence, initially zero"`
	Limit int    `json:"limit" jsonschema:"Maximum events, 1 through 1000"`
}
type transcriptInput struct {
	JobID  string `json:"jobId"`
	Offset int64  `json:"offset"`
	Limit  int    `json:"limit" jsonschema:"Maximum bytes, 1 through 1048576"`
}

func add[T any](s *Server, name, description string, handler func(context.Context, T) (any, error)) {
	destructive := name == "submit_job" || name == "cancel_job"
	readOnly := !destructive && name != "import_artifact"
	openWorld := false
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: name != "new_request_id", OpenWorldHint: &openWorld}
	mcp.AddTool(s.Server, &mcp.Tool{Name: name, Description: description, Annotations: annotations}, func(ctx context.Context, _ *mcp.CallToolRequest, in T) (*mcp.CallToolResult, any, error) {
		out, err := handler(ctx, in)
		return nil, out, err
	})
}
func (s *Server) register() {
	add(s, "capabilities", "Read station identity, network addresses, executor and deployment-task capabilities.", func(ctx context.Context, _ emptyInput) (any, error) { return s.client.Capabilities(ctx) })
	add(s, "list_artifacts", "List artifacts already verified by the station.", func(ctx context.Context, _ emptyInput) (any, error) {
		items, err := s.client.ListArtifacts(ctx)
		return map[string]any{"artifacts": items}, err
	})
	add(s, "get_artifact", "Read one artifact manifest and metadata.", func(ctx context.Context, in artifactInput) (any, error) {
		return s.client.GetArtifact(ctx, in.ArtifactID)
	})
	add(s, "discover_targets", "Discover physical target identities without resetting hardware. Select cable serial and device index explicitly.", func(ctx context.Context, in targetsInput) (any, error) {
		if in.HWServerURL == "" {
			in.HWServerURL = "tcp:127.0.0.1:3121"
		}
		items, err := s.client.Targets(ctx, in.HWServerURL)
		return map[string]any{"targets": items}, err
	})
	add(s, "list_jobs", "List durable station jobs.", func(ctx context.Context, _ emptyInput) (any, error) {
		items, err := s.client.ListJobs(ctx)
		return map[string]any{"jobs": items}, err
	})
	add(s, "get_job", "Read a durable job's current state.", func(ctx context.Context, in jobInput) (any, error) { return s.client.GetJob(ctx, in.JobID) })
	add(s, "new_request_id", "Generate an idempotency key. Retain this key and the exact submission request before submit_job.", func(_ context.Context, _ emptyInput) (any, error) {
		id, err := stationclient.NewRequestID()
		return map[string]any{"requestId": id}, err
	})
	add(s, "submit_job", "Submit a hardware-resetting job only after explicit user authorization. Requires requestId plus cable serial and device index from discovery. Retry only the identical request with the same requestId.", func(ctx context.Context, in submitInput) (any, error) {
		if !in.AcknowledgeReset {
			return nil, errors.New("explicit user authorization to reset the selected physical target is required")
		}
		if strings.TrimSpace(in.Request.TargetID) == "" || strings.TrimSpace(in.Request.TargetCableSerial) == "" || strings.TrimSpace(in.Request.TargetDeviceIndex) == "" {
			return nil, errors.New("explicit targetId, targetCableSerial and targetDeviceIndex from discovery are required")
		}
		job, err := s.client.Submit(ctx, in.Request)
		if err != nil {
			return nil, fmt.Errorf("submission requestId %s: %w; reconcile with get_submission before changing the request or generating another key", in.Request.RequestID, err)
		}
		return job, nil
	})
	add(s, "get_submission", "Reconcile an ambiguous submit using the retained requestId. A missing key means no accepted job was recorded at lookup time; retry the identical request with that same key.", func(ctx context.Context, in submissionInput) (any, error) {
		return s.client.Submission(ctx, in.RequestID)
	})
	add(s, "job_events", "Read a bounded event page. Pass the returned nextAfter as after for the next page. Cancellation stops this read only.", func(ctx context.Context, in eventsInput) (any, error) {
		events, err := s.client.Events(ctx, in.JobID, in.After, in.Limit)
		next := in.After
		if len(events) > 0 {
			next = events[len(events)-1].Sequence
		}
		return map[string]any{"events": events, "nextAfter": next}, err
	})
	add(s, "cancel_job", "Explicitly cancel a durable job, including its active executor. Disconnecting MCP never invokes this tool automatically.", func(ctx context.Context, in jobInput) (any, error) { return s.client.Cancel(ctx, in.JobID) })
	add(s, "serial_ports", "Discover local serial ports and stable USB identities.", func(ctx context.Context, _ emptyInput) (any, error) { return s.client.SerialPorts(ctx) })
	add(s, "job_transcript", "Read a bounded serial transcript page. Data is base64 encoded; use nextOffset for continuation. Cancellation stops observation only.", func(ctx context.Context, in transcriptInput) (any, error) {
		return s.client.Transcript(ctx, in.JobID, in.Offset, in.Limit)
	})
	if s.root != nil {
		add(s, "import_artifact", "Import a regular artifact file beneath the host-configured import root. Only relative paths are accepted; station archive verification remains mandatory.", func(ctx context.Context, in importInput) (any, error) { return s.importArtifact(ctx, in.Path) })
	}
}

func (s *Server) importArtifact(ctx context.Context, path string) (any, error) {
	// Reject traversal spelling as well as escape attempts. Root also protects the
	// final open against symlink races and links pointing outside the root.
	if path == "" || !filepath.IsLocal(path) || strings.Contains(path, "\\") {
		return nil, errors.New("artifact path must be relative beneath the configured root")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return nil, errors.New("artifact path traversal is forbidden")
		}
	}
	entry, err := s.root.Stat(path)
	if err != nil || !entry.Mode().IsRegular() {
		return nil, errors.New("artifact must be a readable regular file within the configured import root")
	}
	file, err := openRegular(s.root, path)
	if err != nil {
		return nil, errors.New("artifact must be a readable regular file within the configured import root")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("artifact must be a regular file")
	}
	if info.Size() > s.maxImport {
		return nil, errors.New("artifact exceeds configured import size limit")
	}
	// LimitReader also bounds files growing after Stat. Over-limit data is cut
	// short and rejected by the station's archive integrity verification.
	return s.client.ImportArtifact(ctx, io.LimitReader(file, s.maxImport))
}
