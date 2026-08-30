package app

import (
	"errors"
	"fmt"

	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
)

// Templates lists the catalog: names, descriptions, and full schemas. The
// entries are STARTERS — creating from one copies its source into the new
// config, which owns it from then on (Amendment 3: templating is a property
// of the config source, not a special object).
func (a *App) Templates() ([]catalog.Template, error) { return catalog.Templates() }

// SourcePath is a configuration's template source file (tier 3 only).
func (a *App) SourcePath(name string) string { return cfgstore.SourcePath(a.Dir, name) }

// ConfigSource returns a configuration's template source; ok is false for a
// plain config.
func (a *App) ConfigSource(name string) (string, bool, error) {
	return cfgstore.Source(a.Dir, name)
}

// CreateFromCatalog copies a catalog entry's SOURCE into a new tier-3
// configuration, rendered with the given initial values (they seed the
// fresh default preset — a preset owns ALL of a config's values). The
// result is immediately user-editable source; nothing remembers the
// catalog afterward.
func (a *App) CreateFromCatalog(name, template string, values map[string]any) error {
	t, err := catalog.Get(template)
	if err != nil {
		return err
	}
	return cfgstore.CreateWithSource(a.Dir, name, t.Source(), values)
}

// renderPreset renders a tier-3 configuration's source with one preset's
// bag: unknown fields pruned (another preset's bag may predate a schema
// edit), defaults filled, secrets excluded from the inputs. Errors are the
// caller's values or source being wrong — BadRequest-marked in catalog.
func (a *App) renderPreset(info cfgstore.Info, preset string) (string, error) {
	src, ok, err := cfgstore.Source(a.Dir, info.Name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", state.BadRequest(fmt.Errorf("config %q has no template source", info.Name))
	}
	t, err := catalog.ParseSource(src)
	if err != nil {
		return "", err
	}
	return t.Render(t.PruneUnknown(info.Meta.Presets[preset]), cfgstore.StorageDir(a.Dir))
}

// WriteConfigSource is the tier-3 source save pipeline: parse the schema,
// reconcile EVERY preset's bag with it (unknown fields pruned, newly
// defaulted fields filled — values live in presets, so a schema edit
// touches them all), render with the ACTIVE preset's bag, store source +
// rendered + reconciled presets, then validate the rendered config against
// the collector — and on rejection put everything back (nothing-was-saved,
// the same honesty a yaml save's activation gives). Values are NOT part of
// this save: the preset routes carry them (Amendment 4). validate=false is
// the same escape hatch as the yaml route's: the source must still parse
// and render, only the collector's verdict is skipped, the running
// collector is never touched, and runningStale reports when the active
// running collector now runs a stale version.
func (a *App) WriteConfigSource(name, src string, validate bool) (runningStale bool, err error) {
	prevSrc, hadSrc, err := cfgstore.Source(a.Dir, name)
	if err != nil {
		return false, err
	}
	info, prevYAML, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return false, err
	}
	if src == "" {
		return false, state.BadRequest(fmt.Errorf("no source given; preset values are saved through the preset routes"))
	}
	// LooksLikeSource, not IsSource: this route only ever receives source
	// attempts, so text merely SHAPED like front matter (either form) gets
	// the loud parse error — a broken YAML schema must not masquerade as
	// "plain yaml" here the way it quietly demotes on the paste path.
	if !catalog.LooksLikeSource(src) {
		return false, state.BadRequest(fmt.Errorf("config source has no schema front matter; write plain configs through the yaml editor"))
	}
	t, err := catalog.ParseSource(src)
	if err != nil {
		return false, err
	}
	m := info.Meta
	m.Presets = make(map[string]map[string]any, len(info.Meta.Presets))
	for pname, bag := range info.Meta.Presets {
		m.Presets[pname] = t.Reconcile(bag)
	}
	rendered, err := t.Render(t.PruneUnknown(m.Presets[m.ActivePreset]), cfgstore.StorageDir(a.Dir))
	if err != nil {
		return false, err
	}
	if err := cfgstore.WriteSource(a.Dir, name, src, rendered); err != nil {
		return false, err
	}
	if err := cfgstore.WriteMeta(a.Dir, name, m); err != nil {
		return false, err
	}
	if !validate {
		if a.isActive(name) {
			if running, _ := launchd.Running(); running {
				return true, nil
			}
		}
		return false, nil
	}
	if verr := a.ValidateConfig(name); verr != nil {
		// Nothing-was-saved: the collector rejected the render, so the prior
		// pair and meta (or prior plain yaml, when the source was pasted over
		// a plain config) go back exactly as they were.
		var rerr error
		if hadSrc {
			rerr = cfgstore.WriteSource(a.Dir, name, prevSrc, prevYAML)
		} else {
			rerr = cfgstore.WriteYAML(a.Dir, name, prevYAML)
		}
		if rerr == nil {
			rerr = cfgstore.WriteMeta(a.Dir, name, info.Meta)
		}
		if rerr != nil {
			return false, errors.Join(verr, fmt.Errorf("and restoring the previous config failed: %w", rerr))
		}
		return false, verr
	}
	return false, a.reactivateIf(name)
}
