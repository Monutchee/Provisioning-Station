// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/buildinfo"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/mcpserver"
	"github.com/Monutchee/Provisioning-Station/internal/servicecontrol"
	"github.com/Monutchee/Provisioning-Station/internal/stationclient"
	"github.com/Monutchee/Provisioning-Station/internal/tui"
	"github.com/charmbracelet/x/term"
)

const usage = `Usage: mps [--url URL] [--token TOKEN | --token-file FILE] COMMAND

  tui                              Interactive station console (terminal default)
  service start|stop|status         Manage the installed machine service
  artifact import PATH|list|inspect ID
  storage status|cleanup (--dry-run|--apply)
  target list [--hw-server-url URL]
  job submit --request FILE --acknowledge-reset [--request-id KEY]
  job list|inspect ID|cancel ID|reconcile REQUEST_ID
  job logs [--after N] [--limit N] [--follow] ID
  mcp [--import-root DIR]           Stdio MCP, existing local service only
  version

Global flags precede COMMAND; command flags precede positional arguments.
Environment: MPS_URL, MPS_TOKEN, MPS_TOKEN_FILE.
`

type dependencies struct {
	http        *http.Client
	start       func(context.Context) error
	stop        func(context.Context) error
	query       func(context.Context) (servicecontrol.Status, error)
	interactive bool
	launchTUI   func(context.Context, *stationclient.Client) error
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, dependencies{interactive: term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mps:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, errOut io.Writer, dep dependencies) error {
	if dep.start == nil {
		dep.start = servicecontrol.Start
	}
	if dep.stop == nil {
		dep.stop = servicecontrol.Stop
	}
	if dep.query == nil {
		dep.query = servicecontrol.Query
	}
	if dep.launchTUI == nil {
		dep.launchTUI = tui.Run
	}
	flags := flag.NewFlagSet("mps", flag.ContinueOnError)
	flags.SetOutput(errOut)
	endpoint := flags.String("url", env("MPS_URL", stationclient.DefaultURL), "station HTTP origin")
	token := flags.String("token", os.Getenv("MPS_TOKEN"), "API token (prefer environment or token file)")
	tokenFile := flags.String("token-file", os.Getenv("MPS_TOKEN_FILE"), "file containing API token")
	flags.Usage = func() { fmt.Fprint(out, usage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = flags.Args()
	if len(args) == 0 {
		if !dep.interactive {
			fmt.Fprint(out, usage)
			return nil
		}
		args = []string{"tui"}
	}
	if args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintln(out, buildinfo.String("mps"))
		return nil
	}
	if args[0] == "service" {
		if len(args) != 2 {
			return errors.New("service requires start, stop, or status")
		}
		switch args[1] {
		case "start":
			return dep.start(ctx)
		case "stop":
			return dep.stop(ctx)
		case "status":
			status, err := dep.query(ctx)
			if err != nil {
				return err
			}
			return printJSON(out, status)
		default:
			return errors.New("service requires start, stop, or status")
		}
	}
	if *token != "" && *tokenFile != "" {
		return errors.New("configure either MPS_TOKEN/--token or MPS_TOKEN_FILE/--token-file, not both")
	}
	if *tokenFile != "" {
		f, err := os.Open(*tokenFile)
		if err != nil {
			return errors.New("cannot read configured token file; set MPS_TOKEN or a readable MPS_TOKEN_FILE")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 4097))
		_ = f.Close()
		if readErr != nil || len(data) > 4096 {
			return errors.New("invalid token file")
		}
		*token = strings.TrimSpace(string(data))
		if *token == "" {
			return errors.New("token file is empty")
		}
	}
	config := stationclient.Config{URL: *endpoint, Token: *token, HTTPClient: dep.http}
	if args[0] == "mcp" {
		f := commandFlags("mcp", errOut)
		root := f.String("import-root", "", "absolute host-approved artifact import directory; absent disables import")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected mcp arguments")
		}
		server, err := mcpserver.New(mcpserver.Config{Station: config, Version: buildinfo.Version, ImportRoot: *root})
		if err != nil {
			return err
		}
		defer server.Close()
		return server.RunStdio(ctx)
	}
	client, err := stationclient.New(config)
	if err != nil {
		return err
	}
	switch args[0] {
	case "tui", "status", "artifact", "target", "job", "serial", "storage":
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	start := (func(context.Context) error)(nil)
	if args[0] == "tui" {
		start = dep.start
	}
	if err := stationclient.EnsureReady(ctx, client, stationclient.ReadinessConfig{URL: *endpoint, Start: start}); err != nil {
		return err
	}
	switch args[0] {
	case "tui":
		if len(args) != 1 {
			return errors.New("tui accepts no arguments")
		}
		return dep.launchTUI(ctx, client)
	case "status":
		value, err := client.Capabilities(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "serial":
		if len(args) != 2 || args[1] != "ports" {
			return errors.New("use serial ports")
		}
		value, err := client.SerialPorts(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "storage":
		return storageCommand(ctx, client, args[1:], out, errOut)
	case "artifact":
		return artifactCommand(ctx, client, args[1:], out)
	case "target":
		if len(args) < 2 || args[1] != "list" {
			return errors.New("use target list")
		}
		f := commandFlags("target list", errOut)
		hw := f.String("hw-server-url", "tcp:127.0.0.1:3121", "hardware-server URL")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected target arguments")
		}
		value, err := client.Targets(ctx, *hw)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "job":
		return jobCommand(ctx, client, args[1:], out, errOut)
	}
	return nil
}
func artifactCommand(ctx context.Context, c *stationclient.Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("artifact requires import, list, or inspect")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("use artifact list")
		}
		value, err := c.ListArtifacts(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "inspect":
		if len(args) != 2 {
			return errors.New("use artifact inspect ID")
		}
		value, err := c.GetArtifact(ctx, args[1])
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "import":
		if len(args) != 2 {
			return errors.New("use artifact import PATH")
		}
		file, err := stationclient.OpenArtifactFile(args[1])
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("artifact must be a regular file")
		}
		value, err := c.ImportArtifact(ctx, file)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	}
	return errors.New("artifact requires import, list, or inspect")
}
func jobCommand(ctx context.Context, c *stationclient.Client, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("job requires submit, list, inspect, logs, cancel, or reconcile")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("use job list")
		}
		value, err := c.ListJobs(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "inspect", "cancel", "reconcile":
		if len(args) != 2 {
			return fmt.Errorf("use job %s ID", args[0])
		}
		var value any
		var err error
		switch args[0] {
		case "inspect":
			value, err = c.GetJob(ctx, args[1])
		case "cancel":
			value, err = c.Cancel(ctx, args[1])
		case "reconcile":
			value, err = c.Submission(ctx, args[1])
		}
		if err != nil {
			return err
		}
		return printJSON(out, value)
	case "submit":
		f := commandFlags("job submit", errOut)
		path := f.String("request", "", "JSON file containing jobs.Request")
		ack := f.Bool("acknowledge-reset", false, "explicitly authorize resetting the selected physical target")
		key := f.String("request-id", "", "retained request ID for exact retries")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *path == "" {
			return errors.New("use job submit --request FILE --acknowledge-reset [--request-id KEY]")
		}
		if !*ack {
			return errors.New("--acknowledge-reset is required after authorizing this physical target reset")
		}
		var request jobs.Request
		if err := readRequest(*path, &request); err != nil {
			return err
		}
		if strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.TargetCableSerial) == "" || strings.TrimSpace(request.TargetDeviceIndex) == "" {
			return errors.New("request requires targetId, targetCableSerial and targetDeviceIndex from target list")
		}
		if *key != "" {
			if request.RequestID != "" && request.RequestID != *key {
				return errors.New("request file and --request-id disagree")
			}
			request.RequestID = *key
		}
		if request.RequestID == "" {
			var err error
			request.RequestID, err = stationclient.NewRequestID()
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(errOut, "requestId:", request.RequestID)
		value, err := c.Submit(ctx, request)
		if err != nil {
			return fmt.Errorf("%w; retain request file and requestId %s; run mps job reconcile %s, then retry the identical request with --request-id %s", err, request.RequestID, request.RequestID, request.RequestID)
		}
		return printJSON(out, value)
	case "logs":
		f := commandFlags("job logs", errOut)
		after := f.Int("after", 0, "last received event sequence")
		limit := f.Int("limit", 100, "page size 1..1000")
		follow := f.Bool("follow", false, "follow until terminal; interruption stops observation only")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return errors.New("use job logs [--after N --limit N --follow] ID")
		}
		id := f.Arg(0)
		for {
			events, err := c.Events(ctx, id, *after, *limit)
			if err != nil {
				return err
			}
			for _, event := range events {
				if err := printJSON(out, event); err != nil {
					return err
				}
				*after = event.Sequence
			}
			if !*follow {
				return nil
			}
			job, err := c.GetJob(ctx, id)
			if err != nil {
				return err
			}
			if job.State.Terminal() && len(events) < *limit {
				return nil
			}
			if len(events) == *limit {
				continue
			}
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return errors.New("unknown job command")
}
func readRequest(path string, out *jobs.Request) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("request JSON exceeds 1 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request file must contain one JSON object")
	}
	return nil
}
func commandFlags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}
func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func storageCommand(ctx context.Context, c *stationclient.Client, args []string, out, errOut io.Writer) error {
	if len(args) == 1 && args[0] == "status" {
		value, err := c.Storage(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, value)
	}
	if len(args) == 0 || args[0] != "cleanup" {
		return errors.New("use storage status or storage cleanup (--dry-run|--apply)")
	}
	f := commandFlags("storage cleanup", errOut)
	dryRun := f.Bool("dry-run", false, "preview eligible jobs/artifacts and reclaimable bytes")
	apply := f.Bool("apply", false, "delete eligible historical jobs and artifact payloads")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *dryRun == *apply {
		return errors.New("storage cleanup requires exactly one of --dry-run or --apply")
	}
	value, err := c.CleanupStorage(ctx, *apply)
	if err != nil {
		return err
	}
	return printJSON(out, value)
}
