package app

import (
	"fmt"
	"path/filepath"

	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/state"
)

// Templates lists the catalog: names, descriptions, and full schemas (the
// creation form renders itself from these).
func (a *App) Templates() ([]catalog.Template, error) { return catalog.Templates() }

// storageDir is where a rendered offline queue keeps its file_storage
// state: inside the state directory, next to configs/ and logs/. It bakes
// into the rendered YAML as a literal — the config stays plain.
func (a *App) storageDir() string { return filepath.Join(a.Dir, "storage") }

// CreateFromTemplate renders a catalog template ONCE with the given knob
// values and creates the result as a new configuration. Only secrets
// survive as ${env:...} references; meta records the template name and the
// normalized knobs so "change options" can re-render later.
func (a *App) CreateFromTemplate(name, template string, knobs map[string]any) error {
	t, err := catalog.Get(template)
	if err != nil {
		return err
	}
	norm, err := t.NormalizeKnobs(knobs)
	if err != nil {
		return err
	}
	yaml, err := t.Render(norm, a.storageDir())
	if err != nil {
		return err
	}
	return cfgstore.CreateFromTemplate(a.Dir, name, yaml, template, norm)
}

// ReRender re-renders a template-born configuration with new knob values,
// refusing one whose YAML was hand-edited since the last render — the
// mirror of Sync. Presets are kept; the running configuration is
// re-applied. nil knobs re-render with the stored ones (e.g. after a
// template upgrade).
func (a *App) ReRender(name string, knobs map[string]any) error {
	return a.reRender(name, knobs, false)
}

// ReRenderForce re-renders even a hand-edited configuration, discarding the
// local edits — the mirror of Resync.
func (a *App) ReRenderForce(name string, knobs map[string]any) error {
	return a.reRender(name, knobs, true)
}

func (a *App) reRender(name string, knobs map[string]any, force bool) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if info.Meta.Template == "" {
		return state.BadRequest(fmt.Errorf("config %q was not created from a template", name))
	}
	if !force && info.Modified {
		return state.BadRequest(fmt.Errorf("config %q is locally modified; use a forced re-render to discard local edits", name))
	}
	if knobs == nil {
		knobs = info.Meta.Knobs
	}
	t, err := catalog.Get(info.Meta.Template)
	if err != nil {
		return err
	}
	norm, err := t.NormalizeKnobs(knobs)
	if err != nil {
		return err
	}
	yaml, err := t.Render(norm, a.storageDir())
	if err != nil {
		return err
	}
	if err := cfgstore.SetRendered(a.Dir, name, yaml, norm); err != nil {
		return err
	}
	return a.reactivateIf(name)
}
