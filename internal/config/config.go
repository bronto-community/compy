// Package config manages compy's collector configuration: the base
// template, per-backend fragments, collector arg construction, and
// last-good snapshot/restore.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/bronto-io/compy/internal/state"
)

// MergeGate is the collector feature gate enabling the append-merge
// behavior that lets multiple --config fragments compose additively.
const MergeGate = "confmap.enableMergeAppendOption"

var baseTemplate = template.Must(template.New("base").Parse(baseYAML))

const baseYAML = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:{{.GRPCPort}}
      http:
        endpoint: 127.0.0.1:{{.HTTPPort}}
exporters:
  nop:
processors:
  batch:
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [nop]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [nop]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [nop]
`

// EnsureBase writes config/base.yaml rendered from s, but ONLY if it does
// not already exist, and returns its path.
//
// ponytail: base.yaml not regenerated on port change; edit base.yaml +
// settings together or delete base.yaml to regenerate
func EnsureBase(dir string, s state.Settings) (string, error) {
	path := filepath.Join(dir, "config", "base.yaml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var buf bytes.Buffer
	if err := baseTemplate.Execute(&buf, s); err != nil {
		return "", err
	}
	if err := state.WriteFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// BackendPath returns the path a backend's config fragment lives at.
func BackendPath(dir, name string) string {
	return filepath.Join(dir, "config", "backends", name+".yaml")
}

// WriteBackend writes a backend's config fragment atomically. It rejects
// invalid backend names.
func WriteBackend(dir, name string, yaml []byte) error {
	if !state.ValidBackendName(name) {
		return fmt.Errorf("invalid backend name %q", name)
	}
	return state.WriteFileAtomic(BackendPath(dir, name), yaml, 0o600)
}

// ListBackends returns the sorted names of all backend fragments on disk.
func ListBackends(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "config", "backends"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	slices.Sort(names)
	return names, nil
}

// DeleteBackend removes a backend's config fragment. It rejects invalid
// backend names (guards against path traversal via BackendPath).
func DeleteBackend(dir, name string) error {
	if !state.ValidBackendName(name) {
		return fmt.Errorf("invalid backend name %q", name)
	}
	if err := os.Remove(BackendPath(dir, name)); err != nil {
		return fmt.Errorf("delete backend %q: %w", name, err)
	}
	return nil
}

// Args returns the full collector argument list for the given settings.
func Args(dir string, s state.Settings) ([]string, error) {
	if s.RawMode {
		return []string{
			"--config", filepath.Join(dir, "config", "custom.yaml"),
			"--feature-gates=" + MergeGate,
		}, nil
	}
	args := []string{"--config", filepath.Join(dir, "config", "base.yaml")}
	enabled := slices.Clone(s.Enabled)
	slices.Sort(enabled)
	for _, name := range enabled {
		path := BackendPath(dir, name)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("backend %q config fragment missing: %w", name, err)
		}
		args = append(args, "--config", path)
	}
	args = append(args, "--feature-gates="+MergeGate)
	return args, nil
}

// SnapshotLastGood copies the config/ tree and settings.json into
// last-good/, replacing any prior snapshot.
func SnapshotLastGood(dir string) error {
	dst := filepath.Join(dir, "last-good")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(dir, "config"), filepath.Join(dst, "config")); err != nil {
		return err
	}
	return copyFile(filepath.Join(dir, "settings.json"), filepath.Join(dst, "settings.json"), 0o600)
}

// RestoreLastGood copies last-good/ back over config/ and settings.json.
// It errors if no snapshot exists.
func RestoreLastGood(dir string) error {
	src := filepath.Join(dir, "last-good")
	// state.Dir() pre-creates last-good/ empty, so its mere existence proves
	// nothing; settings.json only lands there via SnapshotLastGood.
	if _, err := os.Stat(filepath.Join(src, "settings.json")); err != nil {
		return fmt.Errorf("no last-good snapshot to restore")
	}
	if err := os.RemoveAll(filepath.Join(dir, "config")); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(src, "config"), filepath.Join(dir, "config")); err != nil {
		return err
	}
	return copyFile(filepath.Join(src, "settings.json"), filepath.Join(dir, "settings.json"), 0o600)
}

// copyTree recursively copies src to dst. A missing src is a no-op.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, 0o600)
	})
}

// copyFile copies src to dst atomically. A missing src is a no-op.
func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return state.WriteFileAtomic(dst, data, perm)
}
