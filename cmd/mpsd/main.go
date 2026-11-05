// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/daemon"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

const usage = `Monutchee Provisioning Station

Usage:
  mpsd serve [options]
  mpsd hw-server [options] [-- hw_server-options...]
  mpsd inspect [options] <artifact.tar.gz>
  mpsd token [options]
  mpsd version
  mpsd service  (Windows SCM host)

Run "mpsd <command> -help" for command options.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mpsd:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return runServe(nil)
	}
	switch arguments[0] {
	case "service":
		return runService(arguments[1:])
	case "serve":
		return runServe(arguments[1:])
	case "hw-server":
		return runXilinxHWServer(arguments[1:])
	case "inspect":
		return runInspect(arguments[1:])
	case "token":
		return runToken(arguments[1:])
	case "version", "--version", "-version":
		fmt.Printf("mpsd %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", arguments[0], usage)
	}
}

func runXilinxHWServer(arguments []string) error {
	return runXilinxHWServerWithExecutor(arguments, executeXilinxHWServer)
}

func runXilinxHWServerWithExecutor(arguments []string, execute func(string, []string) error) error {
	flags := flag.NewFlagSet("hw-server", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := os.Getenv("MPS_HW_SERVER")
	listen := environmentOr("MPS_HW_SERVER_LISTEN", "tcp:127.0.0.1:3121")
	check := false
	flags.StringVar(&path, "hw-server-path", path, "explicit Xilinx hw_server executable path")
	flags.StringVar(&listen, "listen", listen, "Xilinx hw_server TCP listen URL; empty uses the vendor default")
	flags.BoolVar(&check, "check", false, "resolve hw_server and exit without starting it")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if check && flags.NArg() != 0 {
		return fmt.Errorf("hw-server --check accepts no hw_server arguments")
	}
	if listen != "" {
		if err := xsdb.ValidateHWServerURL(listen); err != nil {
			return fmt.Errorf("invalid hw_server listen URL: %w", err)
		}
	}
	resolved, err := xsdb.ResolveHWServer(path)
	if err != nil {
		return err
	}
	if check {
		fmt.Println(resolved)
		return nil
	}

	hardwareServerArguments := flags.Args()
	if listen != "" {
		hardwareServerArguments = append([]string{"-s" + listen}, hardwareServerArguments...)
	}
	fmt.Printf("Starting Xilinx hw_server: %s\n", resolved)
	if err := execute(resolved, hardwareServerArguments); err != nil {
		return fmt.Errorf("run Xilinx hw_server: %w", err)
	}
	return nil
}

func runServe(arguments []string) error {
	defaults, err := defaultServeConfig()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	config := defaults
	flags.StringVar(&config.HTTPListen, "http-listen", config.HTTPListen, "HTTP API and UI listen address")
	flags.StringVar(&config.TFTPListen, "tftp-listen", config.TFTPListen, "TFTP listen address used during a job")
	flags.StringVar(&config.DataDir, "data-dir", config.DataDir, "persistent Station data directory")
	flags.StringVar(&config.XSDBPath, "xsdb-path", config.XSDBPath, "explicit xsdb executable path")
	flags.StringVar(&config.TokenFile, "api-token-file", config.TokenFile, "file containing the API bearer token")
	flags.DurationVar(&config.JobTimeout, "job-timeout", config.JobTimeout, "maximum duration of one provisioning job")
	flags.Int64Var(&config.MaxCompressedBytes, "max-artifact-bytes", config.MaxCompressedBytes, "maximum compressed artifact size")
	flags.Int64Var(&config.MaxUncompressedBytes, "max-unpacked-bytes", config.MaxUncompressedBytes, "maximum extracted artifact size")
	flags.IntVar(&config.SerialBaud, "serial-baud", config.SerialBaud, "default FTDI serial console baud rate")
	flags.Int64Var(&config.MaxConsoleLogBytes, "max-console-log-bytes", config.MaxConsoleLogBytes, "maximum retained serial transcript bytes per job")
	flags.BoolVar(&config.OpenBrowser, "open-browser", false, "open the local dashboard in the default browser")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("serve accepts no positional arguments")
	}
	return serve(config)
}

type serveConfig = daemon.Config

func defaultServeConfig() (serveConfig, error) {
	configDirectory, err := machineDataDir()
	if err != nil {
		return serveConfig{}, fmt.Errorf("find machine state directory: %w", err)
	}
	limits := artifact.DefaultLimits()
	serialBaud, err := environmentInt("MPS_SERIAL_BAUD", serialconsole.DefaultBaudRate)
	if err != nil {
		return serveConfig{}, err
	}
	maxConsoleLogBytes, err := environmentInt64("MPS_MAX_CONSOLE_LOG_BYTES", serialconsole.DefaultLogBytes)
	if err != nil {
		return serveConfig{}, err
	}
	return serveConfig{
		Version:              version,
		HTTPListen:           environmentOr("MPS_HTTP_LISTEN", "0.0.0.0:8042"),
		TFTPListen:           environmentOr("MPS_TFTP_LISTEN", ":69"),
		DataDir:              environmentOr("MPS_DATA_DIR", configDirectory),
		XSDBPath:             os.Getenv("MPS_XSDB"),
		TokenFile:            os.Getenv("MPS_TOKEN_FILE"),
		APIToken:             os.Getenv("MPS_TOKEN"),
		JobTimeout:           10 * time.Minute,
		MaxCompressedBytes:   limits.MaxCompressedBytes,
		MaxUncompressedBytes: limits.MaxUncompressedBytes,
		SerialBaud:           serialBaud,
		MaxConsoleLogBytes:   maxConsoleLogBytes,
	}, nil
}

func serve(config serveConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveContext(ctx, config, nil)
}

func serveContext(ctx context.Context, config serveConfig, ready func()) error {
	return daemon.Run(ctx, config, ready)
}

func runInspect(arguments []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	asJSON := flags.Bool("json", false, "print the validated artifact as JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("inspect requires exactly one artifact path")
	}
	temporary, err := os.MkdirTemp("", "mpsd-inspect-")
	if err != nil {
		return fmt.Errorf("create inspection directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	store, err := artifact.OpenStore(temporary, artifact.DefaultLimits())
	if err != nil {
		return err
	}
	stored, err := store.ImportFile(context.Background(), flags.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(stored)
	}
	metadata := stored.Manifest.Artifact
	fmt.Printf("Artifact:  %s\n", metadata.Name)
	fmt.Printf("ID:        %s\n", stored.ID)
	fmt.Printf("Target:    %s/%s (%s)\n", metadata.Vendor, metadata.Operation, metadata.Machine)
	fmt.Printf("Build:     %s (%s)\n", metadata.BuildID, metadata.CreatedUTC)
	fmt.Printf("Executor:  %s -> %s\n", stored.Manifest.Executor.Type, stored.Manifest.Executor.Entrypoint)
	fmt.Printf("Files:     %d verified payload files\n", len(stored.Manifest.Files))
	if stored.HasSignature {
		fmt.Println("Signature: present (verification is reserved for the release pipeline)")
	} else {
		fmt.Println("Signature: absent")
	}
	return nil
}

func runToken(arguments []string) error {
	defaults, err := defaultServeConfig()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dataDir := flags.String("data-dir", defaults.DataDir, "Station data directory containing the managed API token")
	tokenFile := flags.String("api-token-file", defaults.TokenFile, "explicit API bearer token file")
	service := flags.Bool("service", false, "use the Debian system service token file")
	rotate := flags.Bool("rotate", false, "replace the managed API token and print the new value")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("token accepts no positional arguments")
	}

	config := defaults
	config.DataDir = *dataDir
	config.TokenFile = *tokenFile
	if *rotate {
		path, err := apiTokenFile(config, *service)
		if err != nil {
			return err
		}
		token, err := rotateAPIToken(path)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	}
	token, err := existingAPIToken(config, *service)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

func apiTokenFile(config serveConfig, service bool) (string, error) {
	if service {
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("--service is only available for Debian service installations")
		}
		config.APIToken = ""
		if config.TokenFile == "" {
			config.TokenFile = "/var/lib/mps/api-token"
		}
	}
	if config.APIToken != "" && config.TokenFile == "" {
		return "", fmt.Errorf("cannot rotate MPS_TOKEN; configure a managed token file")
	}
	if config.APIToken != "" && config.TokenFile != "" {
		return "", fmt.Errorf("set either MPS_TOKEN or --api-token-file, not both")
	}
	if config.TokenFile != "" {
		return config.TokenFile, nil
	}
	return filepath.Join(config.DataDir, "api-token"), nil
}

func existingAPIToken(config serveConfig, service bool) (string, error) {
	if service {
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("--service is only available for Debian service installations")
		}
		config.APIToken = ""
		if config.TokenFile == "" {
			config.TokenFile = "/var/lib/mps/api-token"
		}
	}
	if config.APIToken != "" || config.TokenFile != "" {
		token, err := loadAPIToken(config.APIToken, config.TokenFile)
		if err != nil {
			return "", tokenReadError(config.TokenFile, service, err)
		}
		return token, nil
	}

	tokenFile := filepath.Join(config.DataDir, "api-token")
	token, err := loadAPIToken("", tokenFile)
	if err != nil {
		return "", tokenReadError(tokenFile, service, err)
	}
	return token, nil
}

func tokenReadError(tokenFile string, service bool, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		if runtime.GOOS == "linux" && !service {
			return fmt.Errorf("API token does not exist at %s; start the Station first, or use 'sudo mpsd token --service' for the Debian service", tokenFile)
		}
		return fmt.Errorf("API token does not exist at %s; start the Station first", tokenFile)
	}
	if service && errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("read Debian service API token: %w (try: sudo mpsd token --service)", err)
	}
	return err
}

func loadAPIToken(token, path string) (string, error) { return daemon.LoadAPIToken(token, path) }
func resolveAPIToken(config serveConfig) (string, string, error) {
	return daemon.ResolveAPIToken(config)
}
func loadOrCreateAPIToken(path string) (string, error) { return daemon.LoadOrCreateAPIToken(path) }
func rotateAPIToken(path string) (string, error)       { return daemon.RotateAPIToken(path) }
func isLoopbackListen(address string) bool             { return daemon.IsLoopbackListen(address) }
func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func environmentInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func environmentInt64(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}
