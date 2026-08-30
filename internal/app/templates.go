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
// configuration, rendered with the given knob values (nil = the schema's
// defaults). The result is immediately user-editable source; nothing
// remembers the catalog afterward.
func (a *App) CreateFromCatalog(name, template string, knobs map[string]any) error {
	t, err := catalog.Get(template)
	if err != nil {
		return err
	}
	return cfgstore.CreateWithSource(a.Dir, name, t.Source(), knobs)
}

// WriteConfigSource is the tier-3 save pipeline: parse the schema, apply the
// knobs (stored ones when knobs is nil; removed fields pruned, new fields
// defaulted), render, store source + rendered + knobs, then validate the
// rendered config against the collector — and on rejection put everything
// back (nothing-was-saved, the same honesty a yaml save's activation gives).
// An empty src is a knob-only save over the stored source (the form's path);
// both dirty is one call carrying both — the source applies first, then the
// knobs. validate=false is the same escape hatch as the yaml route's:
// nothing is validated, the running collector is never touched, and
// runningStale reports when the active running collector now runs a stale
// version.
func (a *App) WriteConfigSource(name, src string, knobs map[string]any, validate bool) (runningStale bool, err error) {
	prevSrc, hadSrc, err := cfgstore.Source(a.Dir, name)
	if err != nil {
		return false, err
	}
	info, prevYAML, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return false, err
	}
	if src == "" {
		if !hadSrc {
			return false, state.BadRequest(fmt.Errorf("config %q has no template source; a knob save needs a templated config", name))
		}
		src = prevSrc
	} else if !catalog.IsSource(src) {
		return false, state.BadRequest(fmt.Errorf("config source has no schema front matter; write plain configs through the yaml editor"))
	}
	t, err := catalog.ParseSource(src)
	if err != nil {
		return false, err
	}
	if knobs == nil {
		knobs = info.Meta.Knobs
	}
	norm, err := t.NormalizeKnobs(t.PruneUnknown(knobs))
	if err != nil {
		return false, err
	}
	rendered, err := t.Render(norm, cfgstore.StorageDir(a.Dir))
	if err != nil {
		return false, err
	}
	if err := cfgstore.WriteSource(a.Dir, name, src, rendered, norm); err != nil {
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
		// pair (or prior plain yaml, when the source was pasted over a plain
		// config) goes back exactly as it was.
		var rerr error
		if hadSrc {
			rerr = cfgstore.WriteSource(a.Dir, name, prevSrc, prevYAML, info.Meta.Knobs)
		} else {
			rerr = cfgstore.WriteYAML(a.Dir, name, prevYAML)
		}
		if rerr != nil {
			return false, errors.Join(verr, fmt.Errorf("and restoring the previous config failed: %w", rerr))
		}
		return false, verr
	}
	return false, a.reactivateIf(name)
}
