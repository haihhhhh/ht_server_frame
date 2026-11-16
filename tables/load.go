package tables

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
)

func load[T any](ctx context.Context, source fs.FS, manifest Manifest, opts Options[T]) (*Snapshot[T], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != opts.SchemaVersion || manifest.CodeVersion != opts.CodeVersion {
		return nil, ErrVersion
	}
	if manifest.DataVersion == "" || manifest.ToolVersion == "" {
		return nil, ErrIntegrity
	}
	if len(manifest.Files) == 0 {
		return nil, ErrIntegrity
	}
	if len(manifest.Files) > opts.MaxFiles || len(opts.RequiredFiles) > opts.MaxFiles {
		return nil, ErrLimit
	}
	names := make(map[string]bool, len(manifest.Files))
	digests := make(map[string][]byte, len(manifest.Files))
	for _, file := range manifest.Files {
		if !fs.ValidPath(file.Name) || file.Name == "." || names[file.Name] {
			return nil, ErrIntegrity
		}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, ErrIntegrity
		}
		names[file.Name] = true
		digests[file.Name] = digest
	}
	for _, name := range opts.RequiredFiles {
		if !names[name] {
			return nil, fmt.Errorf("%w: missing required file", ErrIntegrity)
		}
	}
	files := make(map[string][]byte, len(manifest.Files))
	remaining := opts.MaxTotalBytes
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := min(opts.MaxFileBytes, remaining)
		data, err := readBounded(ctx, source, file.Name, limit)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if !equalDigest(digest[:], digests[file.Name]) {
			return nil, fmt.Errorf("%w: checksum mismatch", ErrIntegrity)
		}
		remaining -= int64(len(data))
		files[file.Name] = data
	}
	value, err := opts.Decode(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("tables: decode: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = opts.Validate(ctx, value); err != nil {
		return nil, fmt.Errorf("tables: validate: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	copy := opts.Clone(value)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &Snapshot[T]{
		Version{manifest.DataVersion, manifest.SchemaVersion, manifest.ToolVersion, manifest.CodeVersion},
		copy,
		opts.Clone,
	}, nil
}

func equalDigest(a, b []byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func readBounded(ctx context.Context, source fs.FS, name string, limit int64) (data []byte, err error) {
	f, err := source.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: file unavailable", ErrIntegrity)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			data = nil
			err = fmt.Errorf("%w: file close failed", ErrIntegrity)
		}
	}()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrIntegrity
	}
	if info.Size() < 0 || info.Size() > limit {
		return nil, ErrLimit
	}
	r := contextReader{ctx, f}
	data, err = io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	// 额外探测一个字节，避免 limit+1 运算可能导致的溢出。
	var extra [1]byte
	n, probeErr := io.ReadFull(r, extra[:])
	if n != 0 {
		return nil, ErrLimit
	}
	if probeErr != io.EOF {
		return nil, probeErr
	}
	return data, nil
}
