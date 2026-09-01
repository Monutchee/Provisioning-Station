// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/httpapi"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/runner"
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
  mnc-station serve [options]
  mnc-station inspect [options] <artifact.tar.gz>
  mnc-station token [options]
  mnc-station version

Run "mnc-station <command> -help" for command options.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mnc-station:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return runServe(nil)
	}
	switch arguments[0] {
	case "serve":
		return runServe(arguments[1:])
	case "inspect":
		return runInspect(arguments[1:])
	case "token":
		return runToken(arguments[1:])
	case "version", "--version", "-version":
		fmt.Printf("mnc-station %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", arguments[0], usage)
	}
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

type serveConfig struct {
	HTTPListen           string
	TFTPListen           string
	DataDir              string
	XSDBPath             string
	TokenFile            string
	APIToken             string
	JobTimeout           time.Duration
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	SerialBaud           int
	MaxConsoleLogBytes   int64
	OpenBrowser          bool
}

func defaultServeConfig() (serveConfig, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return serveConfig{}, fmt.Errorf("find user configuration directory: %w", err)
	}
	limits := artifact.DefaultLimits()
	serialBaud, err := environmentInt("MNC_STATION_SERIAL_BAUD", serialconsole.DefaultBaudRate)
	if err != nil {
		return serveConfig{}, err
	}
	maxConsoleLogBytes, err := environmentInt64("MNC_STATION_MAX_CONSOLE_LOG_BYTES", serialconsole.DefaultLogBytes)
	if err != nil {
		return serveConfig{}, err
	}
	return serveConfig{
		HTTPListen:           environmentOr("MNC_STATION_HTTP_LISTEN", "0.0.0.0:8042"),
		TFTPListen:           environmentOr("MNC_STATION_TFTP_LISTEN", ":69"),
		DataDir:              environmentOr("MNC_STATION_DATA_DIR", filepath.Join(configDirectory, "Monutchee", "Provisioning-Station")),
		XSDBPath:             os.Getenv("MNC_XSDB"),
		TokenFile:            os.Getenv("MNC_STATION_TOKEN_FILE"),
		APIToken:             os.Getenv("MNC_STATION_TOKEN"),
		JobTimeout:           10 * time.Minute,
		MaxCompressedBytes:   limits.MaxCompressedBytes,
		MaxUncompressedBytes: limits.MaxUncompressedBytes,
		SerialBaud:           serialBaud,
		MaxConsoleLogBytes:   maxConsoleLogBytes,
	}, nil
}

func serve(config serveConfig) error {
	if config.HTTPListen == "" || config.TFTPListen == "" || config.DataDir == "" {
		return fmt.Errorf("listen addresses and data directory must not be empty")
	}
	if config.JobTimeout <= 0 {
		return fmt.Errorf("job timeout must be positive")
	}
	if err := serialconsole.ValidateBaudRate(config.SerialBaud); err != nil {
		return err
	}
	if config.MaxConsoleLogBytes <= 0 {
		return fmt.Errorf("maximum console log bytes must be positive")
	}
	token, tokenFile, err := resolveAPIToken(config)
	if err != nil {
		return err
	}

	limits := artifact.DefaultLimits()
	limits.MaxCompressedBytes = config.MaxCompressedBytes
	limits.MaxUncompressedBytes = config.MaxUncompressedBytes
	store, err := artifact.OpenStore(filepath.Join(config.DataDir, "store"), limits)
	if err != nil {
		return err
	}
	executor := xsdb.Executor{Path: config.XSDBPath}
	consoleManager, err := serialconsole.New(serialconsole.Config{DefaultBaud: config.SerialBaud})
	if err != nil {
		return err
	}
	defer consoleManager.Close()
	hardwareRunner, err := runner.NewXilinx(runner.XilinxConfig{
		Executor: executor, TFTPListen: config.TFTPListen, JobTimeout: config.JobTimeout,
		Serial: consoleManager,
	})
	if err != nil {
		return err
	}
	jobManager, err := jobs.OpenManager(
		filepath.Join(config.DataDir, "jobs"), store, hardwareRunner,
		jobs.WithSerialConsole(consoleManager, config.MaxConsoleLogBytes),
	)
	if err != nil {
		return err
	}
	defer jobManager.Close()
	api, err := httpapi.New(httpapi.Config{
		Version: version, APIToken: token, TFTPListen: config.TFTPListen, XSDB: executor,
		Serial: consoleManager, MaxConsoleLogBytes: config.MaxConsoleLogBytes,
	}, store, jobManager)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", config.HTTPListen)
	if err != nil {
		return fmt.Errorf("listen for Station HTTP API on %s: %w", config.HTTPListen, err)
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	url := listenerURL(listener.Addr())
	fmt.Printf("Monutchee Provisioning Station %s\n", version)
	fmt.Printf("Dashboard: %s\n", url)
	fmt.Printf("TFTP jobs: %s\n", config.TFTPListen)
	fmt.Printf("Serial console: FT2232H channel B at %d baud (job logs up to %d bytes)\n", config.SerialBaud, config.MaxConsoleLogBytes)
	if tokenFile != "" {
		fmt.Printf("API authentication: required (token file %s)\n", tokenFile)
	} else if token != "" {
		fmt.Println("API authentication: required")
	}
	if path, resolveErr := executor.Resolve(); resolveErr == nil {
		fmt.Printf("XSDB: %s\n", path)
	} else {
		fmt.Printf("XSDB: unavailable (%s)\n", resolveErr)
	}
	if config.OpenBrowser {
		go func() {
			time.Sleep(150 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				fmt.Fprintln(os.Stderr, "mnc-station: open browser:", err)
			}
		}()
	}

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- httpServer.Serve(listener) }()
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-shutdownContext.Done():
		context, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(context); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		return nil
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Station HTTP API: %w", err)
	}
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
	temporary, err := os.MkdirTemp("", "mnc-station-inspect-")
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
			config.TokenFile = "/var/lib/mnc-station/api-token"
		}
	}
	if config.APIToken != "" && config.TokenFile == "" {
		return "", fmt.Errorf("cannot rotate MNC_STATION_TOKEN; configure a managed token file")
	}
	if config.APIToken != "" && config.TokenFile != "" {
		return "", fmt.Errorf("set either MNC_STATION_TOKEN or --api-token-file, not both")
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
			config.TokenFile = "/var/lib/mnc-station/api-token"
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
			return fmt.Errorf("API token does not exist at %s; start the Station first, or use 'sudo mnc-station token --service' for the Debian service", tokenFile)
		}
		return fmt.Errorf("API token does not exist at %s; start the Station first", tokenFile)
	}
	if service && errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("read Debian service API token: %w (try: sudo mnc-station token --service)", err)
	}
	return err
}

func loadAPIToken(environmentToken, tokenFile string) (string, error) {
	if environmentToken != "" && tokenFile != "" {
		return "", fmt.Errorf("set either MNC_STATION_TOKEN or --api-token-file, not both")
	}
	token := environmentToken
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read API token file: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("API token file must not be empty: %s", tokenFile)
		}
	}
	if strings.ContainsAny(token, "\r\n\x00") {
		return "", fmt.Errorf("API token must be a single non-empty line")
	}
	if token != "" && len(token) < 16 {
		return "", fmt.Errorf("API token must contain at least 16 characters")
	}
	return token, nil
}

func resolveAPIToken(config serveConfig) (string, string, error) {
	token, err := loadAPIToken(config.APIToken, config.TokenFile)
	if err != nil {
		return "", "", err
	}
	if token != "" {
		return token, config.TokenFile, nil
	}
	if isLoopbackListen(config.HTTPListen) {
		return "", "", nil
	}

	tokenFile := filepath.Join(config.DataDir, "api-token")
	token, err = loadOrCreateAPIToken(tokenFile)
	if err != nil {
		return "", "", fmt.Errorf("prepare API token for non-loopback listener %q: %w", config.HTTPListen, err)
	}
	return token, tokenFile, nil
}

func loadOrCreateAPIToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("managed API token path is not a regular file: %s", path)
		}
		return loadAPIToken("", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect managed API token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create API token directory: %w", err)
	}

	token, err := generateAPIToken()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return loadAPIToken("", path)
	}
	if err != nil {
		return "", fmt.Errorf("create managed API token: %w", err)
	}
	if _, err := fmt.Fprintln(file, token); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write managed API token: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close managed API token: %w", err)
	}
	return token, nil
}

func rotateAPIToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect managed API token: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("managed API token path is not a regular file: %s", path)
	}
	token, err := generateAPIToken()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("open managed API token for rotation: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure rotated API token: %w", err)
	}
	if _, err := fmt.Fprintln(file, token); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write rotated API token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync rotated API token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close rotated API token: %w", err)
	}
	return token, nil
}

func generateAPIToken() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenerURL(address net.Addr) string {
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return "http://" + address.String() + "/"
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	return command.Start()
}

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
