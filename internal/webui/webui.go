// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed static/*
var assets embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Cache-Control", "no-cache")
		clean := path.Clean(request.URL.Path)
		if clean == "." || clean == "/" {
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			response.Header().Set("Content-Length", strconv.Itoa(len(index)))
			if request.Method == http.MethodGet {
				_, _ = response.Write(index)
			}
			return
		} else if strings.Contains(clean, "..") {
			http.NotFound(response, request)
			return
		}
		files.ServeHTTP(response, request)
	})
}
