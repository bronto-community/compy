package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/bronto-io/compy/internal/cfgstore"
	"github.com/bronto-io/compy/internal/distro"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
)

// legacySettings are the v1 settings.json fields v2 dropped; they only exist
// to drive the one-way migration below.
type legacySettings struct {
	Enabled []string `json:"enabled"`
	RawMode bool     `json:"raw_mode"`
}

// migrateLegacy converts a v1 state dir (config/base.yaml + enabled
// config/backends/*.yaml fragments) into a single "migrated" configuration,
// then archives the v1 tree as legacy-v1/. One-way, logged, and a no-op once
// config/ is gone.
func (a *App) migrateLegacy() error {
	legacy := filepath.Join(a.Dir, "config")
	if _, err := os.Stat(filepath.Join(legacy, "backends")); err != nil {
		return nil
	}

	var old legacySettings
	if data, err := os.ReadFile(filepath.Join(a.Dir, "settings.json")); err == nil {
		_ = json.Unmarshal(data, &old) // best effort: a broken file just means no enabled backends
	}

	yaml, how := a.renderLegacy(legacy, old)
	if _, _, err := cfgstore.Get(a.Dir, "migrated"); err != nil {
		if err := cfgstore.Create(a.Dir, "migrated", yaml); err != nil {
			return err
		}
	}

	activated := len(old.Enabled) > 0 || old.RawMode
	if activated {
		s, err := state.LoadSettings()
		if err != nil {
			return err
		}
		s.ActiveConfig = "migrated"
		if err := state.SaveSettings(s); err != nil {
			return err
		}
	}

	archive := filepath.Join(a.Dir, "legacy-v1")
	if err := os.RemoveAll(archive); err != nil {
		return err
	}
	if err := os.Rename(legacy, archive); err != nil {
		return err
	}

	// The v1 LaunchAgent still points at the files just archived, with
	// KeepAlive on: left alone it crash-loops on a missing config and
	// telemetry stops silently. Repoint it at the migrated configuration —
	// or stop it outright when nothing was enabled.
	note := "the collector job was stopped (nothing was enabled)"
	if activated {
		note = "made it active and restarted the collector"
		if err := a.Apply(); err != nil {
			note = fmt.Sprintf("made it active, but restarting the collector failed (%v) — run `compy apply`", err)
		}
	} else if err := launchd.Uninstall(); err != nil {
		note = fmt.Sprintf("could not stop the old collector job (%v) — run `compy service uninstall`", err)
	}
	fmt.Fprintf(os.Stderr, "compy: migrated v1 backends into configuration %q (%s); %s; old files archived in %s\n",
		"migrated", how, note, archive)
	return nil
}

// renderLegacy produces the migrated YAML: the v1 arg list run through the
// collector's print-initial-config (the merge the old model relied on), or —
// when no collector binary is resolvable without a download, or it fails —
// a plain copy of the old base.yaml. The second return value says which.
func (a *App) renderLegacy(legacy string, old legacySettings) (yaml, how string) {
	base := filepath.Join(legacy, "base.yaml")
	fallback := func(reason string) (string, string) {
		data, err := os.ReadFile(base)
		if err != nil {
			return "# compy: the v1 config could not be read during migration\n", "empty: " + err.Error()
		}
		return string(data), "copied base.yaml: " + reason
	}

	bin := a.installedDistro()
	if bin == "" {
		return fallback("no collector binary available")
	}

	args := []string{"print-initial-config", "--feature-gates=confmap.enableMergeAppendOption,otelcol.printInitialConfig"}
	if old.RawMode {
		args = append(args, "--config", filepath.Join(legacy, "custom.yaml"))
	} else {
		args = append(args, "--config", base)
		for _, name := range slices.Sorted(slices.Values(old.Enabled)) {
			frag := filepath.Join(legacy, "backends", name+".yaml")
			if _, err := os.Stat(frag); err == nil {
				args = append(args, "--config", frag)
			}
		}
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil || len(out) == 0 {
		return fallback(fmt.Sprintf("print-initial-config failed: %v", err))
	}
	return string(out), "rendered by the collector"
}

// installedDistro returns the path of the selected distro's binary if it is
// already on disk. Migration never triggers a download.
func (a *App) installedDistro() string {
	s, err := state.LoadSettings()
	if err != nil {
		return ""
	}
	reg, err := distro.Registry(a.Dir)
	if err != nil {
		return ""
	}
	for _, d := range reg {
		if d.Name != s.Distro || d.Path == "" {
			continue
		}
		if _, err := os.Stat(d.Path); err == nil {
			return d.Path
		}
	}
	return ""
}
