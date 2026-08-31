// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
)

const metadataFilename = "artifact.json"

type StoredArtifact struct {
	ID                string    `json:"id"`
	ImportedUTC       time.Time `json:"importedUtc"`
	CompressedBytes   int64     `json:"compressedBytes"`
	UncompressedBytes int64     `json:"uncompressedBytes"`
	HasSignature      bool      `json:"hasSignature"`
	Manifest          Manifest  `json:"manifest"`
	ArchivePath       string    `json:"-"`
	RootPath          string    `json:"-"`
}

type Store struct {
	root      string
	artifacts string
	temporary string
	limits    Limits
}

func OpenStore(root string, limits Limits) (*Store, error) {
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact store path: %w", err)
	}
	store := &Store{
		root:      root,
		artifacts: filepath.Join(root, "artifacts"),
		temporary: filepath.Join(root, "tmp"),
		limits:    limits,
	}
	for _, directory := range []string{store.root, store.artifacts, store.temporary} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create artifact store directory: %w", err)
		}
	}
	return store, nil
}

func (store *Store) Import(ctx context.Context, source io.Reader) (StoredArtifact, error) {
	upload, err := os.CreateTemp(store.temporary, ".upload-*.tar.gz")
	if err != nil {
		return StoredArtifact{}, fmt.Errorf("create temporary upload: %w", err)
	}
	uploadPath := upload.Name()
	defer os.Remove(uploadPath)

	digest := sha256.New()
	limited := &io.LimitedReader{R: source, N: store.limits.MaxCompressedBytes + 1}
	written, copyErr := io.Copy(io.MultiWriter(upload, digest), limited)
	closeErr := upload.Close()
	if copyErr != nil {
		return StoredArtifact{}, fmt.Errorf("store artifact upload: %w", copyErr)
	}
	if closeErr != nil {
		return StoredArtifact{}, fmt.Errorf("close artifact upload: %w", closeErr)
	}
	if written > store.limits.MaxCompressedBytes {
		return StoredArtifact{}, fmt.Errorf("artifact exceeds the compressed size limit of %d bytes", store.limits.MaxCompressedBytes)
	}
	if written == 0 {
		return StoredArtifact{}, fmt.Errorf("artifact upload is empty")
	}
	id := hex.EncodeToString(digest.Sum(nil))
	if existing, err := store.Load(id); err == nil {
		return existing, nil
	}

	staging, err := os.MkdirTemp(store.artifacts, ".import-")
	if err != nil {
		return StoredArtifact{}, fmt.Errorf("create artifact staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	payloadRoot := filepath.Join(staging, "payload")
	extraction, err := ValidateAndExtract(ctx, uploadPath, payloadRoot, store.limits)
	if err != nil {
		return StoredArtifact{}, err
	}

	archivePath := filepath.Join(staging, "artifact.tar.gz")
	if err := moveFile(uploadPath, archivePath); err != nil {
		return StoredArtifact{}, fmt.Errorf("retain artifact archive: %w", err)
	}
	record := StoredArtifact{
		ID:                id,
		ImportedUTC:       time.Now().UTC(),
		CompressedBytes:   written,
		UncompressedBytes: extraction.UncompressedBytes,
		HasSignature:      extraction.HasSignature,
		Manifest:          extraction.Manifest,
	}
	if err := writeJSON(filepath.Join(staging, metadataFilename), record); err != nil {
		return StoredArtifact{}, err
	}

	finalDirectory := filepath.Join(store.artifacts, id)
	if err := os.Rename(staging, finalDirectory); err != nil {
		if existing, loadErr := store.Load(id); loadErr == nil {
			return existing, nil
		}
		return StoredArtifact{}, fmt.Errorf("publish artifact: %w", err)
	}
	record.ArchivePath = filepath.Join(finalDirectory, "artifact.tar.gz")
	record.RootPath = filepath.Join(finalDirectory, "payload")
	return record, nil
}

func (store *Store) ImportFile(ctx context.Context, path string) (StoredArtifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return StoredArtifact{}, fmt.Errorf("open artifact %q: %w", path, err)
	}
	defer file.Close()
	return store.Import(ctx, file)
}

func (store *Store) Load(id string) (StoredArtifact, error) {
	if !sha256Pattern.MatchString(id) {
		return StoredArtifact{}, fmt.Errorf("artifact ID must be a SHA-256 digest")
	}
	directory := filepath.Join(store.artifacts, id)
	data, err := os.ReadFile(filepath.Join(directory, metadataFilename))
	if err != nil {
		return StoredArtifact{}, fmt.Errorf("artifact %s was not found", id)
	}
	var record StoredArtifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return StoredArtifact{}, fmt.Errorf("decode stored artifact metadata: %w", err)
	}
	if record.ID != id {
		return StoredArtifact{}, fmt.Errorf("stored artifact ID does not match its directory")
	}
	if err := record.Manifest.Validate(); err != nil {
		return StoredArtifact{}, fmt.Errorf("stored artifact manifest: %w", err)
	}
	record.ArchivePath = filepath.Join(directory, "artifact.tar.gz")
	record.RootPath = filepath.Join(directory, "payload")
	return record, nil
}

func (store *Store) List() ([]StoredArtifact, error) {
	entries, err := os.ReadDir(store.artifacts)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	artifacts := make([]StoredArtifact, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !sha256Pattern.MatchString(entry.Name()) {
			continue
		}
		record, err := store.Load(entry.Name())
		if err != nil {
			continue
		}
		artifacts = append(artifacts, record)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].ImportedUTC.After(artifacts[j].ImportedUTC)
	})
	return artifacts, nil
}

// Verify checks the retained archive and extracted payload immediately before
// a hardware job can consume them. Import validation is not enough because
// local storage may have been modified after import.
func (store *Store) Verify(ctx context.Context, record StoredArtifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedDirectory := filepath.Join(store.artifacts, record.ID)
	if filepath.Clean(filepath.Dir(record.ArchivePath)) != expectedDirectory ||
		filepath.Clean(filepath.Dir(record.RootPath)) != expectedDirectory {
		return fmt.Errorf("artifact paths do not belong to the content-addressed store")
	}
	archiveFile, err := os.Open(record.ArchivePath)
	if err != nil {
		return fmt.Errorf("open retained artifact archive: %w", err)
	}
	archiveDigest := sha256.New()
	_, copyErr := io.Copy(archiveDigest, contextReader{ctx: ctx, reader: archiveFile})
	closeErr := archiveFile.Close()
	if copyErr != nil {
		return fmt.Errorf("hash retained artifact archive: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close retained artifact archive: %w", closeErr)
	}
	if hex.EncodeToString(archiveDigest.Sum(nil)) != record.ID {
		return fmt.Errorf("retained artifact archive SHA-256 does not match its ID")
	}

	manifestData, err := os.ReadFile(filepath.Join(record.RootPath, ManifestName))
	if err != nil {
		return fmt.Errorf("read stored artifact manifest: %w", err)
	}
	extractedManifest, err := DecodeManifest(manifestData)
	if err != nil {
		return fmt.Errorf("stored artifact manifest: %w", err)
	}
	if !reflect.DeepEqual(extractedManifest, record.Manifest) {
		return fmt.Errorf("stored artifact manifest differs from its metadata")
	}

	actual := make(map[string]ActualFile, len(record.Manifest.Files))
	expected := make(map[string]struct{}, len(record.Manifest.Files)+2)
	expected[ManifestName] = struct{}{}
	if record.HasSignature {
		expected[SignatureName] = struct{}{}
	}
	for name := range record.Manifest.Files {
		expected[name] = struct{}{}
	}
	err = filepath.WalkDir(record.RootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == record.RootPath || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(record.RootPath, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := expected[relative]; !ok {
			return fmt.Errorf("stored artifact contains unexpected path %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("stored artifact path %q is not a regular file", relative)
		}
		delete(expected, relative)
		descriptor, payload := record.Manifest.Files[relative]
		if !payload {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		digest := sha256.New()
		written, copyErr := io.Copy(digest, contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		mode := descriptor.Mode
		if runtime.GOOS != "windows" {
			mode = fmt.Sprintf("%04o", info.Mode().Perm())
		}
		actual[relative] = ActualFile{
			Size: written, SHA256: hex.EncodeToString(digest.Sum(nil)), Mode: mode,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify stored artifact payload: %w", err)
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for name := range expected {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return fmt.Errorf("stored artifact is missing: %s", strings.Join(missing, ", "))
	}
	if err := record.Manifest.ValidatePayload(actual); err != nil {
		return fmt.Errorf("stored artifact payload: %w", err)
	}
	return nil
}

func (store *Store) Limits() Limits {
	return store.limits
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(data)
}

func moveFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Remove(source)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact metadata: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write artifact metadata: %w", err)
	}
	return nil
}
