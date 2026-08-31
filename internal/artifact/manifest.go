// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaName       = "mnc-station-artifact"
	FormatVersion    = 2
	ManifestName     = "manifest.json"
	SignatureName    = "manifest.sig"
	ManifestMaxBytes = 1 << 20
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Manifest struct {
	Schema        string                    `json:"schema"`
	FormatVersion int                       `json:"formatVersion"`
	Artifact      ArtifactMetadata          `json:"artifact"`
	Executor      ExecutorMetadata          `json:"executor"`
	Files         map[string]FileDescriptor `json:"files"`
}

type ArtifactMetadata struct {
	Name       string `json:"name"`
	Vendor     string `json:"vendor"`
	Operation  string `json:"operation"`
	Product    string `json:"product"`
	Machine    string `json:"machine"`
	Version    string `json:"version"`
	BuildID    string `json:"buildId"`
	CreatedUTC string `json:"createdUtc"`
}

type ExecutorMetadata struct {
	Type       string `json:"type"`
	Entrypoint string `json:"entrypoint"`
	TFTPRoot   string `json:"tftpRoot,omitempty"`
}

type FileDescriptor struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

type ActualFile struct {
	Size   int64
	SHA256 string
	Mode   string
}

func DecodeManifest(data []byte) (Manifest, error) {
	if len(data) == 0 || len(data) > ManifestMaxBytes {
		return Manifest{}, fmt.Errorf("manifest size must be between 1 and %d bytes", ManifestMaxBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.Schema != SchemaName {
		return fmt.Errorf("unsupported manifest schema %q", manifest.Schema)
	}
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported manifest formatVersion %d", manifest.FormatVersion)
	}

	identifiers := map[string]string{
		"artifact.name":      manifest.Artifact.Name,
		"artifact.vendor":    manifest.Artifact.Vendor,
		"artifact.operation": manifest.Artifact.Operation,
		"artifact.product":   manifest.Artifact.Product,
		"artifact.machine":   manifest.Artifact.Machine,
		"artifact.buildId":   manifest.Artifact.BuildID,
		"executor.type":      manifest.Executor.Type,
	}
	for label, value := range identifiers {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s is not a valid identifier: %q", label, value)
		}
	}
	if err := validateText("artifact.version", manifest.Artifact.Version); err != nil {
		return err
	}
	created, err := time.Parse(time.RFC3339, manifest.Artifact.CreatedUTC)
	if err != nil || !strings.HasSuffix(manifest.Artifact.CreatedUTC, "Z") || created.Location() != time.UTC {
		return fmt.Errorf("artifact.createdUtc must be an ISO-8601 UTC timestamp ending in Z")
	}
	if err := ValidateRelativePath(manifest.Executor.Entrypoint); err != nil {
		return fmt.Errorf("executor.entrypoint: %w", err)
	}
	if manifest.Executor.TFTPRoot != "" {
		if err := ValidateRelativePath(manifest.Executor.TFTPRoot); err != nil {
			return fmt.Errorf("executor.tftpRoot: %w", err)
		}
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("files must contain at least one payload file")
	}

	for path, descriptor := range manifest.Files {
		if path == ManifestName || path == SignatureName {
			return fmt.Errorf("files contains reserved path %q", path)
		}
		if err := ValidateRelativePath(path); err != nil {
			return fmt.Errorf("files path %q: %w", path, err)
		}
		if descriptor.Size < 0 {
			return fmt.Errorf("files[%q].size must not be negative", path)
		}
		if !sha256Pattern.MatchString(descriptor.SHA256) {
			return fmt.Errorf("files[%q].sha256 must be 64 lowercase hexadecimal characters", path)
		}
		if descriptor.Mode != "0644" && descriptor.Mode != "0755" {
			return fmt.Errorf("files[%q].mode must be 0644 or 0755", path)
		}
	}

	entrypoint, ok := manifest.Files[manifest.Executor.Entrypoint]
	if !ok {
		return fmt.Errorf("executor entrypoint %q is absent from files", manifest.Executor.Entrypoint)
	}
	if entrypoint.Mode != "0755" {
		return fmt.Errorf("executor entrypoint %q must have mode 0755", manifest.Executor.Entrypoint)
	}
	if root := manifest.Executor.TFTPRoot; root != "" {
		prefix := root + "/"
		found := false
		for path := range manifest.Files {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("executor tftpRoot %q contains no files", root)
		}
	}
	return nil
}

func (manifest Manifest) ValidatePayload(actual map[string]ActualFile) error {
	if len(actual) != len(manifest.Files) {
		return payloadSetError(manifest.Files, actual)
	}
	for path, expected := range manifest.Files {
		observed, ok := actual[path]
		if !ok {
			return payloadSetError(manifest.Files, actual)
		}
		if observed.Size != expected.Size {
			return fmt.Errorf("payload size differs from manifest for %q", path)
		}
		if observed.SHA256 != expected.SHA256 {
			return fmt.Errorf("payload SHA-256 differs from manifest for %q", path)
		}
		if observed.Mode != expected.Mode {
			return fmt.Errorf("payload mode differs from manifest for %q", path)
		}
	}
	return nil
}

func ValidateRelativePath(value string) error {
	if value == "" {
		return fmt.Errorf("path must not be empty")
	}
	if strings.HasPrefix(value, "/") || strings.ContainsAny(value, `\:`) {
		return fmt.Errorf("unsafe or non-portable relative path %q", value)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("unsafe control character in path %q", value)
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe path component in %q", value)
		}
	}
	return nil
}

func SortedPayloadPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Files))
	for path := range manifest.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func validateText(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s must be a single-line string", label)
		}
	}
	return nil
}

func payloadSetError(expected map[string]FileDescriptor, actual map[string]ActualFile) error {
	missing := make([]string, 0)
	extra := make([]string, 0)
	for path := range expected {
		if _, ok := actual[path]; !ok {
			missing = append(missing, path)
		}
	}
	for path := range actual {
		if _, ok := expected[path]; !ok {
			extra = append(extra, path)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("archive payload differs from manifest: missing=%v extra=%v", missing, extra)
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walkValue func() error
	walkValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode manifest JSON: %w", err)
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("decode manifest object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("manifest object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("manifest contains duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walkValue(); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return fmt.Errorf("decode manifest object: %w", err)
			}
		case '[':
			for decoder.More() {
				if err := walkValue(); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return fmt.Errorf("decode manifest array: %w", err)
			}
		default:
			return fmt.Errorf("unexpected manifest JSON delimiter %q", delimiter)
		}
		return nil
	}
	if err := walkValue(); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("manifest contains trailing JSON data")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("manifest contains trailing JSON value")
		}
		return fmt.Errorf("manifest contains trailing data: %w", err)
	}
	return nil
}
