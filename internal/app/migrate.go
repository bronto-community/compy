package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/distro"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
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

	// Stale-recreation guard: a still-running v1 process (typically the old
	// tray, which re-created config/base.yaml on its 5s resync) can rebuild
	// the legacy tree AFTER a completed migration. A genuine v1 upgrade has
	// no v2 state yet — so if v2 state exists, archive the leftovers and
	// change nothing else (no launchd, no settings, no configs). Observed
	// live 2026-08-25: the un-guarded re-run stopped the collector and
	// clobbered the archive.
	// Staleness signals: an active v2 config, or the migrated configuration
	// already existing (covers the nothing-was-enabled migration, where
	// ActiveConfig stays empty). A LoadSettings error falls through to the
	// genuine path — safe either way, since archiveLegacy never clobbers.
	stale := false
	if s, err := state.LoadSettings(); err == nil && s.ActiveConfig != "" {
		stale = true
	} else if _, _, err := cfgstore.Get(a.Dir, "migrated"); err == nil {
		stale = true
	}
	if stale {
		archive, err := a.archiveLegacy(legacy)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "compy: archived stale v1 leftovers (recreated by an old compy process still running?) in %s; nothing else changed\n", archive)
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

	archive, err := a.archiveLegacy(legacy)
	if err != nil {
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

// archiveLegacy moves the legacy tree aside without ever overwriting a
// previous archive: legacy-v1, then legacy-v1.2, .3, ….
func (a *App) archiveLegacy(legacy string) (string, error) {
	base := filepath.Join(a.Dir, "legacy-v1")
	archive := base
	for i := 2; ; i++ {
		if _, err := os.Stat(archive); os.IsNotExist(err) {
			break
		}
		archive = fmt.Sprintf("%s.%d", base, i)
	}
	return archive, os.Rename(legacy, archive)
}

// renderLegacy produces the migrated YAML: the v1 arg list run through the
// collector's print-initial-config (the merge the old model relied on), or —
// when no collector binary is resolvable without a download, or it fails —
// a plain copy of the old base.yaml. The second return value says which.
func (a *App) renderLegacy(legacy string, old legacySettings) (yaml, how string) {
	base := filepath.Join(legacy, "base.yaml")
	fallbackSrc, fallbackName := base, "base.yaml"
	if old.RawMode {
		fallbackSrc, fallbackName = filepath.Join(legacy, "custom.yaml"), "custom.yaml"
	}
	fallback := func(reason string) (string, string) {
		data, err := os.ReadFile(fallbackSrc)
		if err != nil {
			return "# compy: the v1 config could not be read during migration\n", "empty: " + err.Error()
		}
		return string(data), "copied " + fallbackName + ": " + reason
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
	want := effectiveDistro(s)
	for _, d := range reg {
		if d.Name != want || d.Path == "" {
			continue
		}
		if _, err := os.Stat(d.Path); err == nil {
			return d.Path
		}
	}
	return ""
}
