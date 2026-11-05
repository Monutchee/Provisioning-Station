// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func LoadAPIToken(environmentToken, tokenFile string) (string, error) {
	if environmentToken != "" && tokenFile != "" {
		return "", fmt.Errorf("set either MPS_TOKEN or --api-token-file, not both")
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

func ResolveAPIToken(config Config) (string, string, error) {
	token, err := LoadAPIToken(config.APIToken, config.TokenFile)
	if err != nil {
		return "", "", err
	}
	if token != "" {
		return token, config.TokenFile, nil
	}
	if IsLoopbackListen(config.HTTPListen) {
		return "", "", nil
	}

	tokenFile := filepath.Join(config.DataDir, "api-token")
	token, err = LoadOrCreateAPIToken(tokenFile)
	if err != nil {
		return "", "", fmt.Errorf("prepare API token for non-loopback listener %q: %w", config.HTTPListen, err)
	}
	return token, tokenFile, nil
}

func LoadOrCreateAPIToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("managed API token path is not a regular file: %s", path)
		}
		return LoadAPIToken("", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect managed API token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create API token directory: %w", err)
	}

	token, err := GenerateAPIToken()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return LoadAPIToken("", path)
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

func RotateAPIToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect managed API token: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("managed API token path is not a regular file: %s", path)
	}
	token, err := GenerateAPIToken()
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

func GenerateAPIToken() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func IsLoopbackListen(address string) bool {
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
