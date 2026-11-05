// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/artifact"
	"github.com/Monutchee/Provisioning-Station/internal/httpapi"
	"github.com/Monutchee/Provisioning-Station/internal/jobs"
	"github.com/Monutchee/Provisioning-Station/internal/runner"
	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/Monutchee/Provisioning-Station/internal/statelock"
	"github.com/Monutchee/Provisioning-Station/internal/storagebudget"
	"github.com/Monutchee/Provisioning-Station/internal/tasks"
	"github.com/Monutchee/Provisioning-Station/internal/xsdb"
)

type Config struct {
	Version              string
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
	OwnHWServer          bool
}

func Run(shutdownContext context.Context, config Config, ready func()) error {
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
	lock, err := statelock.Acquire(config.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if config.OwnHWServer {
		closeChild, err := startOwnedHWServer(shutdownContext)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mpsd: managed hardware server unavailable:", err)
		} else {
			defer closeChild()
		}
	}
	token, tokenFile, err := ResolveAPIToken(config)
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
	if err := store.CleanStaging(); err != nil {
		return err
	}
	budget := storagebudget.New(config.DataDir, storagebudget.DefaultLimit)
	store.SetBudget(budget)
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
	// Trusted compiled adapters register here. An empty registry deliberately
	// exposes no device-specific operations until their contracts are defined.
	taskRegistry, err := tasks.New()
	if err != nil {
		return err
	}
	jobManager, err := jobs.OpenManager(
		filepath.Join(config.DataDir, "jobs"), store, hardwareRunner,
		jobs.WithSerialConsole(consoleManager, config.MaxConsoleLogBytes),
		jobs.WithDeploymentTasks(taskRegistry),
		jobs.WithStorageBudget(budget),
	)
	if err != nil {
		return err
	}
	defer jobManager.Close()
	api, err := httpapi.New(httpapi.Config{
		Version: config.Version, APIToken: token, TFTPListen: config.TFTPListen, XSDB: executor,
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
	defer httpServer.Close()
	url := listenerURL(listener.Addr())
	fmt.Printf("Monutchee Provisioning Station %s\n", config.Version)
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
				fmt.Fprintln(os.Stderr, "mpsd: open browser:", err)
			}
		}()
	}

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- httpServer.Serve(listener) }()
	if ready != nil {
		ready()
	}
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
