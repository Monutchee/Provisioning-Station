// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type Limits struct {
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	MaxFileBytes         int64
	MaxFiles             int
}

const signatureMaxBytes = 1 << 20

func DefaultLimits() Limits {
	return Limits{
		MaxCompressedBytes:   2 << 30,
		MaxUncompressedBytes: 8 << 30,
		MaxFileBytes:         6 << 30,
		MaxFiles:             256,
	}
}

type Extraction struct {
	Manifest          Manifest
	ManifestBytes     []byte
	HasSignature      bool
	UncompressedBytes int64
	PayloadFiles      int
}

func ValidateAndExtract(ctx context.Context, archivePath, destination string, limits Limits) (Extraction, error) {
	if err := validateLimits(limits); err != nil {
		return Extraction{}, err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return Extraction{}, fmt.Errorf("create extraction directory: %w", err)
	}

	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return Extraction{}, fmt.Errorf("open artifact archive: %w", err)
	}
	defer archiveFile.Close()
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return Extraction{}, fmt.Errorf("open artifact gzip stream: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)

	actual := make(map[string]ActualFile)
	seen := make(map[string]struct{})
	orderedPayload := make([]string, 0)
	var manifestData []byte
	var total int64
	hasSignature := false
	memberIndex := 0

	for {
		if err := ctx.Err(); err != nil {
			return Extraction{}, err
		}
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Extraction{}, fmt.Errorf("read artifact tar stream: %w", err)
		}
		memberIndex++
		if memberIndex > limits.MaxFiles+2 {
			return Extraction{}, fmt.Errorf("artifact contains more than %d payload files", limits.MaxFiles)
		}
		name := header.Name
		if err := ValidateRelativePath(name); err != nil {
			return Extraction{}, fmt.Errorf("archive member: %w", err)
		}
		if _, duplicate := seen[name]; duplicate {
			return Extraction{}, fmt.Errorf("archive contains duplicate path %q", name)
		}
		seen[name] = struct{}{}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return Extraction{}, fmt.Errorf("archive member %q is not a regular file", name)
		}
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			return Extraction{}, fmt.Errorf("archive member %q ownership must be 0/0", name)
		}
		if len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return Extraction{}, fmt.Errorf("archive member %q contains non-normalized extended metadata", name)
		}
		if header.Size < 0 || header.Size > limits.MaxFileBytes {
			return Extraction{}, fmt.Errorf("archive member %q exceeds the per-file limit", name)
		}
		if name == SignatureName && header.Size > signatureMaxBytes {
			return Extraction{}, fmt.Errorf("%s exceeds %d bytes", SignatureName, signatureMaxBytes)
		}
		if total > limits.MaxUncompressedBytes-header.Size {
			return Extraction{}, fmt.Errorf("artifact exceeds the uncompressed size limit")
		}
		total += header.Size

		if header.Mode&^int64(0o777) != 0 {
			return Extraction{}, fmt.Errorf("archive member %q has a non-normalized mode", name)
		}
		mode := fmt.Sprintf("%04o", header.Mode)
		if name == ManifestName || name == SignatureName {
			if mode != "0644" {
				return Extraction{}, fmt.Errorf("archive member %q must have mode 0644", name)
			}
		} else if mode != "0644" && mode != "0755" {
			return Extraction{}, fmt.Errorf("archive member %q must have mode 0644 or 0755", name)
		}

		if memberIndex == 1 && name != ManifestName {
			return Extraction{}, fmt.Errorf("%s must be the first archive member", ManifestName)
		}
		if name == SignatureName {
			if memberIndex != 2 {
				return Extraction{}, fmt.Errorf("%s must immediately follow %s", SignatureName, ManifestName)
			}
			hasSignature = true
		} else if name != ManifestName {
			orderedPayload = append(orderedPayload, name)
		}

		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Extraction{}, fmt.Errorf("create parent for %q: %w", name, err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode&0o777))
		if err != nil {
			return Extraction{}, fmt.Errorf("create extracted file %q: %w", name, err)
		}
		digest := sha256.New()
		writer := io.MultiWriter(output, digest)
		written, copyErr := io.Copy(writer, tarReader)
		closeErr := output.Close()
		if copyErr != nil {
			return Extraction{}, fmt.Errorf("extract %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return Extraction{}, fmt.Errorf("close extracted file %q: %w", name, closeErr)
		}
		if written != header.Size {
			return Extraction{}, fmt.Errorf("archive member %q ended early", name)
		}
		if err := os.Chmod(target, os.FileMode(header.Mode&0o777)); err != nil {
			return Extraction{}, fmt.Errorf("set extracted mode for %q: %w", name, err)
		}

		contentHash := hex.EncodeToString(digest.Sum(nil))
		switch name {
		case ManifestName:
			if header.Size > ManifestMaxBytes {
				return Extraction{}, fmt.Errorf("%s exceeds %d bytes", ManifestName, ManifestMaxBytes)
			}
			manifestData, err = os.ReadFile(target)
			if err != nil {
				return Extraction{}, fmt.Errorf("read extracted manifest: %w", err)
			}
		case SignatureName:
			// Signature verification belongs to the protected release policy.
		default:
			actual[name] = ActualFile{Size: written, SHA256: contentHash, Mode: mode}
		}
	}
	if err := validateTarPadding(ctx, gzipReader); err != nil {
		return Extraction{}, err
	}

	if len(manifestData) == 0 {
		return Extraction{}, fmt.Errorf("archive is missing %s", ManifestName)
	}
	if !sort.StringsAreSorted(orderedPayload) {
		return Extraction{}, fmt.Errorf("archive payload members are not sorted")
	}
	manifest, err := DecodeManifest(manifestData)
	if err != nil {
		return Extraction{}, err
	}
	if err := manifest.ValidatePayload(actual); err != nil {
		return Extraction{}, err
	}
	return Extraction{
		Manifest:          manifest,
		ManifestBytes:     manifestData,
		HasSignature:      hasSignature,
		UncompressedBytes: total,
		PayloadFiles:      len(actual),
	}, nil
}

func validateTarPadding(ctx context.Context, reader io.Reader) error {
	const maximumPadding = 1 << 20
	buffer := make([]byte, 32<<10)
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := reader.Read(buffer)
		for _, value := range buffer[:count] {
			if value != 0 {
				return fmt.Errorf("artifact contains non-zero data after the tar end marker")
			}
		}
		total += count
		if total > maximumPadding {
			return fmt.Errorf("artifact contains more than %d bytes of tar padding", maximumPadding)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read after artifact tar end: %w", err)
		}
	}
}

func validateLimits(limits Limits) error {
	if limits.MaxCompressedBytes <= 0 || limits.MaxUncompressedBytes <= 0 ||
		limits.MaxFileBytes <= 0 || limits.MaxFiles <= 0 {
		return fmt.Errorf("artifact limits must all be positive")
	}
	return nil
}
