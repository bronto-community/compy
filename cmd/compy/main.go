// Command compy manages a local OpenTelemetry Collector: configurations,
// presets, distros, the LaunchAgent service, environment variables, and a
// small web UI.
//
// This file is wiring only — argument parsing and printing. All behavior
// lives in internal/app.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/envvars"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/tray"
	"github.com/bronto-community/compy/internal/version"
	"github.com/bronto-community/compy/internal/webui"
	"github.com/bronto-community/compy/internal/window"
)

const usage = `compy — local OpenTelemetry Collector manager

  compy status [--json]
  compy version
  compy apply | validate | stop | start
  compy adopt-ports [--grpc N] [--http N]
  compy templates
  compy config list
  compy config show|edit|delete|sync|resync|reset <name>
  compy config source|edit-source <name>
  compy config create <name> [--from-url URL]
    (otelbin.io share links import as local configs; quote fragment URLs:
     --from-url 'https://www.otelbin.io/#config=...')
  compy config create <name> --template <template> [--knobs <file.json>]
  compy config copy <src> <dst>
  compy config rename <old> <new>
  compy config sync-all
  compy config set-url <config> <url|-->
  compy use <config> [<preset>]
  compy vars <config>
  compy presets set <config> <preset> KEY=VALUE
  compy presets use|delete <config> <preset>
  compy presets rename <config> <from> <to>
  compy settings
  compy settings set [--grpc-port N] [--http-port N] [--protocol grpc|http/protobuf|http/json]
  compy factory-reset --yes
  compy distro list
  compy distro add <name> <path>
  compy distro set-path <name> <path>
  compy distro remove <name>
  compy distro use|fetch <name>
  compy distro update [--check] <name>
  compy service install|uninstall|status
  compy env [--shell sh|fish|pwsh]
  compy env set-os | unset-os
  compy log [--lines N]
  compy run -- <cmd...>
  compy ui [--port N]
  compy tray [install|uninstall]
  compy window
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "compy: "+errorText(err))
		os.Exit(1)
	}
}

// errorText renders a command's failure, appending the still-running
// reassurance REST/UI users get: a failed activation that put the previous
// setup back says so, instead of leaving the CLI user to guess what runs.
func errorText(err error) string {
	var sr interface{ StillRunning() string }
	if errors.As(err, &sr) {
		return err.Error() + "\nthe previous setup is still running: " + sr.StillRunning()
	}
	return err.Error()
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "status":
		return cmdStatus(rest)
	case "version", "-v", "--version":
		fmt.Println(version.String())
		return nil
	case "apply":
		return withApp(func(a *app.App) error { return a.Apply() })
	case "adopt-ports":
		return cmdAdoptPorts(rest)
	case "stop":
		return withApp(func(a *app.App) error { return a.Stop() })
	case "start":
		return withApp(func(a *app.App) error { return a.Start() })
	case "validate":
		return withApp(func(a *app.App) error {
			if err := a.Validate(); err != nil {
				return err
			}
			fmt.Println("config ok")
			return nil
		})
	case "config":
		return cmdConfig(rest)
	case "templates":
		return withApp(func(a *app.App) error {
			templates, err := a.Templates()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, t := range templates {
				fmt.Fprintf(w, "%s\t%s\n", t.Name, t.Description)
			}
			return w.Flush()
		})
	case "use":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New("use: need <config> [<preset>]")
		}
		preset := ""
		if len(rest) == 2 {
			preset = rest[1]
		}
		return withApp(func(a *app.App) error {
			// Activation pre-flight, warn-only: a required var (no yaml
			// fallback) with no preset value starts a collector that
			// silently drops everything its exporter can't send. The
			// window asks first; the CLI names each gap and proceeds.
			if info, _, err := a.Config(rest[0]); err == nil {
				for _, v := range cfgstore.MissingRequired(a.Dir, info, preset) {
					fmt.Fprintf(os.Stderr, "warning: no value for %s\n", v)
				}
			}
			return a.Activate(rest[0], preset)
		})
	case "vars":
		if len(rest) != 1 {
			return errors.New("vars: need <config>")
		}
		return withApp(func(a *app.App) error { return printVars(a, rest[0]) })
	case "presets":
		return cmdPresets(rest)
	case "settings":
		return cmdSettings(rest)
	case "factory-reset":
		return cmdFactoryReset(rest)
	case "distro":
		return cmdDistro(rest)
	case "service":
		return cmdService(rest)
	case "env":
		return cmdEnv(rest)
	case "log":
		return cmdLog(rest)
	case "run":
		return cmdRun(rest)
	case "ui":
		return cmdUI(rest)
	case "window":
		return withApp(window.Run)
	case "tray":
		return cmdTray(rest)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}

// cmdTray runs the tray inline (no args), or installs/uninstalls a login
// LaunchAgent for it. The tray agent runs at load but is NOT kept alive, so
// quitting from the menu sticks until next login or reinstall; the menu's
// "Remove from Menu Bar" is this uninstall, run from the tray itself.
func cmdTray(args []string) error {
	if len(args) == 0 {
		return withApp(tray.Run)
	}
	switch args[0] {
	case "install":
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		dir, err := state.Dir()
		if err != nil {
			return err
		}
		log := filepath.Join(dir, "logs", "tray.log")
		if err := launchd.InstallAgent(launchd.TrayLabel, bin, []string{"tray"}, log, false, nil); err != nil {
			return err
		}
		fmt.Println("tray installed (starts at login; running now)")
		return nil
	case "uninstall":
		if err := launchd.UninstallAgent(launchd.TrayLabel); err != nil {
			return err
		}
		fmt.Println("tray uninstalled")
		return nil
	default:
		return fmt.Errorf("usage: compy tray [install|uninstall]")
	}
}

func withApp(fn func(*app.App) error) error {
	a, err := app.New()
	if err != nil {
		return err
	}
	a.Progress = printDownload
	return fn(a)
}

// printDownload is the default reporter for automatic distro downloads
// (e.g. `compy use` on a fresh home fetching contrib): a percentage on
// stderr, redrawn only when it changes — the same shape `compy distro
// fetch` prints — with a newline once it completes.
var printDownloadLast = -1

func printDownload(name string, done, total int64) {
	if total <= 0 {
		return
	}
	pct := int(done * 100 / total)
	if pct == printDownloadLast {
		return
	}
	printDownloadLast = pct
	fmt.Fprintf(os.Stderr, "\rdownloading %s… %d%%", name, pct)
	if pct >= 100 {
		fmt.Fprintln(os.Stderr)
		printDownloadLast = -1
	}
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withApp(func(a *app.App) error {
		st, err := a.Status()
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(st)
		}
		running := "stopped"
		if st.Running {
			running = "running"
		}
		config := st.Config
		if config == "" {
			config = "(none)"
		} else if st.Preset != "" {
			config += " (preset " + st.Preset + ")"
		}
		distro := st.Distro
		if distro == "" {
			distro = "(none)"
		}
		// The endpoint line follows the advertised protocol: grpc points at
		// the gRPC port, both http flavors at the HTTP port.
		endPort, otherLine := st.EndpointPort(), fmt.Sprintf("grpc %d", st.GRPCPort)
		if st.Protocol == "grpc" {
			otherLine = fmt.Sprintf("http %d", st.HTTPPort)
		}
		fmt.Printf("service:  %s\nconfig:   %s\ndistro:   %s\nendpoint: http://127.0.0.1:%d (%s; %s)\ncompy:    %s\n",
			running, config, distro, endPort, st.Protocol, otherLine, st.CompyVersion)
		if st.CompyUpdate != "" {
			fmt.Printf("update:   compy %s available — brew upgrade compy\n", st.CompyUpdate)
		}
		// The upgrade window: the plist still names a binary the upgrade
		// deleted. A restart re-resolves it (compy start / compy apply).
		if st.StaleBinary {
			fmt.Println("note:     compy was upgraded — restart the collector to run the new version")
		}
		if len(st.Listening) > 0 {
			fmt.Printf("listening: %s\n", app.PortList(st.Listening))
		}
		// The verdict warns when an app following compy's advertised env
		// would miss this collector; the secondary port missing alone is
		// only a note (the exported endpoint uses the primary one).
		if v := st.Conformance; v != nil {
			advVar := "COMPY_HTTP_PORT"
			if st.Protocol == "grpc" {
				advVar = "COMPY_GRPC_PORT"
			}
			if !v.Conforming {
				listens := "no other ports"
				if len(v.Actual) > 0 {
					listens = app.PortList(v.Actual)
				}
				fmt.Printf("warning: apps point at :%d; this config listens on %s (run `compy adopt-ports`, or bind ${env:%s} in the config)\n", endPort, listens, advVar)
			} else if st.Protocol == "grpc" && v.MissingHTTP {
				fmt.Printf("note: http port :%d is not among this config's listeners\n", st.HTTPPort)
			} else if st.Protocol != "grpc" && v.MissingGRPC {
				fmt.Printf("note: grpc port :%d is not among this config's listeners\n", st.GRPCPort)
			}
		}
		// The drop diagnosis: the running collector reports dropped
		// telemetry AND the active preset is missing required values —
		// the "activate anyway" aftermath validation can't catch.
		if vars := a.DropDiagnosis(); len(vars) > 0 {
			fmt.Printf("warning: dropping data: no value for %s in the active preset\n", strings.Join(vars, ", "))
		}
		return nil
	})
}

// cmdAdoptPorts points the advertised ports at the running config's actual
// OTLP listeners. Without flags the detected ports are classified; an
// ambiguous classification is refused with the candidates named, and
// --grpc/--http resolve it explicitly.
func cmdAdoptPorts(args []string) error {
	fs := flag.NewFlagSet("adopt-ports", flag.ContinueOnError)
	var grpcPort, httpPort int
	fs.IntVar(&grpcPort, "grpc", 0, "which detected port is otlp/grpc")
	fs.IntVar(&httpPort, "http", 0, "which detected port is otlp/http")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var grpcP, httpP *int
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "grpc":
			grpcP = &grpcPort
		case "http":
			httpP = &httpPort
		}
	})
	return withApp(func(a *app.App) error {
		if err := a.AdoptPorts(grpcP, httpP); err != nil {
			return err
		}
		s, err := a.GetSettings()
		if err != nil {
			return err
		}
		fmt.Printf("advertised ports now grpc %d, http %d\n", s.GRPCPort, s.HTTPPort)
		fmt.Println("shipped configs pick the new ports up on their next activation")
		return nil
	})
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config: need a subcommand")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return withApp(listConfigs)
	case "sync-all":
		return withApp(func(a *app.App) error {
			synced, err := a.SyncAll()
			for _, n := range synced {
				fmt.Println("synced", n)
			}
			return err
		})
	case "create":
		if len(rest) == 0 {
			return errors.New("config create: need a name")
		}
		name := rest[0]
		fs := flag.NewFlagSet("config create", flag.ContinueOnError)
		fromURL := fs.String("from-url", "", "fetch the configuration from this URL (otelbin.io share links import as local configs)")
		tmpl := fs.String("template", "", "start from this catalog template — its source is copied in (see `compy templates`)")
		knobsFile := fs.String("knobs", "", "JSON file with the template's knob values")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if *fromURL != "" && *tmpl != "" {
			return errors.New("config create: --from-url and --template are mutually exclusive")
		}
		if *knobsFile != "" && *tmpl == "" {
			return errors.New("config create: --knobs needs --template")
		}
		return withApp(func(a *app.App) error {
			if *fromURL != "" {
				return a.CreateFromURL(name, *fromURL)
			}
			if *tmpl != "" {
				knobs, err := readKnobs(*knobsFile)
				if err != nil {
					return err
				}
				return a.CreateFromCatalog(name, *tmpl, knobs)
			}
			return a.CreateConfig(name, blankConfig)
		})
	case "copy":
		if len(rest) != 2 {
			return errors.New("config copy: need <src> <dst>")
		}
		return withApp(func(a *app.App) error { return a.CopyConfig(rest[0], rest[1]) })
	case "rename":
		if len(rest) != 2 {
			return errors.New("config rename: need <old> <new>")
		}
		return withApp(func(a *app.App) error { return a.RenameConfig(rest[0], rest[1]) })
	case "set-url":
		if len(rest) != 2 {
			return errors.New("config set-url: need <config> <url|-->")
		}
		name, value := rest[0], rest[1]
		p := &value
		if value == "--" {
			cleared := ""
			p = &cleared
		}
		return withApp(func(a *app.App) error { return a.UpdateConfigMeta(name, p) })
	case "show", "edit", "source", "edit-source", "delete", "sync", "resync", "reset":
		if len(rest) != 1 {
			return fmt.Errorf("config %s: need exactly one name", sub)
		}
		name := rest[0]
		return withApp(func(a *app.App) error {
			switch sub {
			case "show":
				_, yaml, err := a.Config(name)
				if err != nil {
					return err
				}
				fmt.Print(yaml)
				return nil
			case "source":
				src, ok, err := a.ConfigSource(name)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("config %q has no template source (it is plain yaml; see `compy config show`)", name)
				}
				fmt.Print(src)
				return nil
			case "edit-source":
				return editSource(a, name)
			case "delete":
				return a.DeleteConfig(name)
			case "sync":
				return a.Sync(name)
			case "resync":
				return a.Resync(name)
			case "reset":
				return a.Reset(name)
			default:
				if !state.ValidBackendName(name) {
					return fmt.Errorf("invalid config name %q", name)
				}
				if info, _, err := a.Config(name); err == nil && info.HasTemplate {
					// The yaml is rendered output; hand it to the editor and
					// the save would silently demote the config to plain.
					return fmt.Errorf("config %q is templated; edit the source with `compy config edit-source %s`", name, name)
				}
				return editThen(a.ConfigPath(name),
					func(s string) error { return a.WriteConfigYAML(name, s) })
			}
		})
	default:
		return fmt.Errorf("config: unknown subcommand %q", sub)
	}
}

// readKnobs parses a template knobs file: a JSON object of knob values
// keyed by field name ("" means no file, i.e. defaults only). JSON is a
// subset of YAML, so a .yaml file holding a JSON object works too.
func readKnobs(path string) (map[string]any, error) {
	knobs := map[string]any{}
	if path == "" {
		return knobs, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &knobs); err != nil {
		return nil, fmt.Errorf("knobs file %s: not a JSON object: %v", path, err)
	}
	return knobs, nil
}

// blankConfig is the starting point for `compy config create` without a URL:
// enough shape to edit, using compy's ports.
const blankConfig = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:${env:COMPY_GRPC_PORT:-14317}
      http:
        endpoint: 127.0.0.1:${env:COMPY_HTTP_PORT:-14318}
exporters:
  debug:
service:
  pipelines:
    traces: {receivers: [otlp], exporters: [debug]}
    metrics: {receivers: [otlp], exporters: [debug]}
    logs: {receivers: [otlp], exporters: [debug]}
`

func listConfigs(a *app.App) error {
	configs, err := a.Configs()
	if err != nil {
		return err
	}
	// A broken active config (e.g. deleted out from under settings.json)
	// shouldn't stop the list from rendering; it just won't be marked active.
	active, _, _ := a.ActiveConfig()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range configs {
		mark := " "
		if c.Name == active {
			mark = "*"
		}
		prov := c.Provenance
		if c.Modified {
			prov += ", modified"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", mark, c.Name, prov, c.Meta.ActivePreset)
	}
	return w.Flush()
}

// typedValue parses a `presets set` value per the config's schema: tier-2
// stays plain strings; a tier-3 field whose type is non-string (toggle,
// multi, the repeat group) takes a JSON literal, and string-shaped fields
// (slug, url, string, choice, secret) take the raw text. An unknown key
// falls through as a string — on tier 3 that IS a free var (a hand-written
// ${env:} ref's value): it lands in the bag under the var's name and is
// exported at activation. Unknown non-string shapes still get named by the
// preset write's own validation.
func typedValue(a *app.App, config, key, raw string) (any, error) {
	src, ok, err := a.ConfigSource(config)
	if err != nil {
		return nil, err
	}
	if !ok {
		return raw, nil // tier 2: env values are strings
	}
	t, err := catalog.ParseSource(src)
	if err != nil {
		return raw, nil // an unparseable source: let the save say so
	}
	if key == "backends" && t.Backends != nil {
		var rows []any
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, fmt.Errorf("presets set: %s takes a JSON array of objects: %v", key, err)
		}
		return rows, nil
	}
	for _, f := range t.Fields {
		if f.Name != key {
			continue
		}
		switch f.Type {
		case "toggle":
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("presets set: %s is a toggle; use true or false", key)
			}
			return b, nil
		case "multi":
			var list []any
			if err := json.Unmarshal([]byte(raw), &list); err != nil {
				return nil, fmt.Errorf("presets set: %s takes a JSON array of strings: %v", key, err)
			}
			return list, nil
		}
		break
	}
	return raw, nil
}

// printVars renders the configuration's variables as a table: one row per
// variable, one column per preset. A tier-3 config's table comes from its
// SCHEMA instead — its values live in preset bags, keyed by field — with
// secrets shown as (set) or -.
func printVars(a *app.App, name string) error {
	info, _, err := a.Config(name)
	if err != nil {
		return err
	}
	presets := make([]string, 0, len(info.Meta.Presets))
	for preset := range info.Meta.Presets {
		presets = append(presets, preset)
	}
	slices.Sort(presets)
	if info.HasTemplate {
		if src, ok, _ := a.ConfigSource(name); ok {
			if t, err := catalog.ParseSource(src); err == nil {
				return printSchemaVars(t, info, presets, cfgstore.StorageDir(a.Dir))
			}
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprint(w, "VARIABLE\tDEFAULT\tDESCRIPTION")
	for _, preset := range presets {
		mark := ""
		if preset == info.Meta.ActivePreset {
			mark = "*"
		}
		fmt.Fprintf(w, "\t%s%s", preset, mark)
	}
	fmt.Fprintln(w)
	for _, v := range info.Vars {
		def := v.Default
		if !v.HasDefault {
			def = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s", v.Name, def, v.Description)
		for _, preset := range presets {
			val, _ := info.Meta.Presets[preset][v.Name].(string)
			fmt.Fprintf(w, "\t%s", val)
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}

// printSchemaVars is the tier-3 `compy vars` table: the schema's fields
// (repeat-group rows expanded per stored entry) with each preset's value —
// secrets only ever as (set) or -, never the value itself — plus one row
// per FREE var (hand-written ${env:} refs in each preset's render, type
// "env"), the tier-2 capability living inside tier 3.
func printSchemaVars(t catalog.Template, info cfgstore.Info, presets []string, storageDir string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprint(w, "FIELD\tTYPE")
	for _, preset := range presets {
		mark := ""
		if preset == info.Meta.ActivePreset {
			mark = "*"
		}
		fmt.Fprintf(w, "\t%s%s", preset, mark)
	}
	fmt.Fprintln(w)
	cell := func(f catalog.Field, v any, present bool) string {
		switch {
		case f.Type == "secret":
			if s, _ := v.(string); strings.TrimSpace(s) != "" {
				return "(set)"
			}
			return "-"
		case !present:
			return "-"
		}
		if s, ok := v.(string); ok {
			return s
		}
		j, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(j)
	}
	row := func(path string, f catalog.Field, at func(bag map[string]any) (any, bool)) {
		fmt.Fprintf(w, "%s\t%s", path, f.Type)
		for _, preset := range presets {
			v, present := at(info.Meta.Presets[preset])
			fmt.Fprintf(w, "\t%s", cell(f, v, present))
		}
		fmt.Fprintln(w)
	}
	for _, f := range t.Fields {
		row(f.Name, f, func(bag map[string]any) (any, bool) {
			v, ok := bag[f.Name]
			return v, ok
		})
	}
	if t.Backends != nil {
		rowCount := 0
		for _, preset := range presets {
			if rows, ok := info.Meta.Presets[preset]["backends"].([]any); ok {
				rowCount = max(rowCount, len(rows))
			}
		}
		for i := 0; i < rowCount; i++ {
			for _, f := range t.Backends.Fields {
				row(fmt.Sprintf("backends[%d].%s", i, f.Name), f, func(bag map[string]any) (any, bool) {
					rows, _ := bag["backends"].([]any)
					if i >= len(rows) {
						return nil, false
					}
					entry, _ := rows[i].(map[string]any)
					v, ok := entry[f.Name]
					return v, ok
				})
			}
		}
	}
	// Free vars, discovered per preset from that preset's own render (an
	// unrenderable bag contributes none), unioned into rows.
	var freeNames []string
	seen := map[string]bool{}
	for _, preset := range presets {
		bag := info.Meta.Presets[preset]
		rendered, err := t.Render(t.PruneUnknown(bag), storageDir)
		if err != nil {
			continue
		}
		for _, v := range t.FreeVars(rendered, bag) {
			if !seen[v.Name] {
				seen[v.Name] = true
				freeNames = append(freeNames, v.Name)
			}
		}
	}
	slices.Sort(freeNames)
	for _, name := range freeNames {
		fmt.Fprintf(w, "%s\tenv", name)
		for _, preset := range presets {
			val, _ := info.Meta.Presets[preset][name].(string)
			fmt.Fprintf(w, "\t%s", val)
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}

// cmdPresets is `compy presets set|use|delete <config> <preset> [KEY=VALUE]`
// and `compy presets rename <config> <from> <to>`.
func cmdPresets(args []string) error {
	if len(args) == 4 {
		switch args[0] {
		case "rename":
			return withApp(func(a *app.App) error { return a.RenamePreset(args[1], args[2], args[3]) })
		case "set":
			key, value, ok := strings.Cut(args[3], "=")
			if !ok {
				return errors.New("presets set: need KEY=VALUE")
			}
			return withApp(func(a *app.App) error {
				typed, err := typedValue(a, args[1], key, value)
				if err != nil {
					return err
				}
				return a.SetVar(args[1], args[2], key, typed)
			})
		}
	}
	if len(args) != 3 {
		return errors.New("presets: need use|delete <config> <preset>, set <config> <preset> KEY=VALUE, or rename <config> <from> <to>")
	}
	sub, name, preset := args[0], args[1], args[2]
	return withApp(func(a *app.App) error {
		switch sub {
		case "use":
			return a.UsePreset(name, preset)
		case "delete":
			return a.DeletePreset(name, preset)
		case "set":
			// Three args reached "set" only by leaving the value off.
			return errors.New("presets set: need <config> <preset> KEY=VALUE")
		default:
			return fmt.Errorf("presets: unknown subcommand %q", sub)
		}
	})
}

// cmdSettings prints compy's global settings (no args), or partially updates
// them via `settings set` (only the flags given are changed).
func cmdSettings(args []string) error {
	if len(args) == 0 {
		return withApp(func(a *app.App) error {
			s, err := a.GetSettings()
			if err != nil {
				return err
			}
			fmt.Printf("grpc-port: %d\nhttp-port: %d\nprotocol: %s\n", s.GRPCPort, s.HTTPPort, s.EffectiveProtocol())
			return nil
		})
	}
	if args[0] != "set" {
		return fmt.Errorf("settings: unknown subcommand %q", args[0])
	}
	fs := flag.NewFlagSet("settings set", flag.ContinueOnError)
	var grpcPort, httpPort int
	var protocol string
	fs.IntVar(&grpcPort, "grpc-port", 0, "gRPC port")
	fs.IntVar(&httpPort, "http-port", 0, "HTTP port")
	fs.StringVar(&protocol, "protocol", "", "advertised OTLP protocol: grpc, http/protobuf, or http/json")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var grpcP, httpP *int
	var protoP *string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "grpc-port":
			grpcP = &grpcPort
		case "http-port":
			httpP = &httpPort
		case "protocol":
			protoP = &protocol
		}
	})
	return withApp(func(a *app.App) error { return a.PutSettings(grpcP, httpP, protoP) })
}

// cmdFactoryReset wipes the state directory and starts over. The CLI has no
// confirm UI, so the destruction is gated on --yes: without it, say what
// would be deleted and how to confirm, and exit non-zero.
func cmdFactoryReset(args []string) error {
	fs := flag.NewFlagSet("factory-reset", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm the reset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withApp(func(a *app.App) error {
		if !*yes {
			return fmt.Errorf("factory-reset deletes everything in %s: all configurations, presets, downloaded collectors, logs, and settings. the shipped configs are recreated.\nrun `compy factory-reset --yes` to confirm", a.Dir)
		}
		if err := a.FactoryReset(); err != nil {
			return err
		}
		fmt.Println("compy was reset to factory settings")
		return nil
	})
}

func cmdDistro(args []string) error {
	if len(args) == 0 {
		return errors.New("distro: need a subcommand")
	}
	switch args[0] {
	case "list":
		return withApp(func(a *app.App) error {
			distros, err := a.Distros()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, d := range distros {
				mark := " "
				if d["selected"] == true {
					mark = "*"
				}
				ver, _ := d["version"].(string)
				note := "user"
				if d["bundled"] == true {
					if d["downloaded"] == true {
						note = "shipped with compy (" + ver + ")"
					} else {
						note = "not built — packaging/collector/build.sh"
					}
				} else if d["definition"] == true {
					switch {
					case d["available"] != true:
						note = "unavailable on this platform"
					case d["downloaded"] == true:
						note = "downloaded (" + ver + ")"
					default:
						// Undownloaded: nothing installed — say what a
						// download would fetch (persisted latest, or the
						// compiled-in pin when no release check has run).
						fv, _ := d["fetch_version"].(string)
						note = "available (downloads " + fv + ")"
					}
				}
				if la, _ := d["latest_available"].(string); la != "" {
					note += " · " + la + " available"
				}
				fmt.Fprintf(w, "%s %s\t%s\t%s\n", mark, d["name"], note, d["path"])
			}
			return w.Flush()
		})
	case "add":
		if len(args) != 3 {
			return errors.New("distro add: need <name> <path>")
		}
		return withApp(func(a *app.App) error { return a.AddDistro(args[1], args[2]) })
	case "set-path":
		if len(args) != 3 {
			return errors.New("distro set-path: need <name> <path>")
		}
		return withApp(func(a *app.App) error {
			warning, err := a.SetDistroPath(args[1], args[2])
			if err != nil {
				return err
			}
			if warning != "" {
				fmt.Fprintln(os.Stderr, "compy:", warning)
			}
			return nil
		})
	case "remove":
		if len(args) != 2 {
			return errors.New("distro remove: need <name>")
		}
		return withApp(func(a *app.App) error {
			reverted, err := a.RemoveDistro(args[1])
			if err != nil {
				return err
			}
			if reverted {
				fmt.Println("reverted to the shipped definition")
			} else {
				fmt.Println("removed")
			}
			return nil
		})
	case "use":
		if len(args) != 2 {
			return errors.New("distro use: need <name>")
		}
		return withApp(func(a *app.App) error { return a.UseDistro(args[1]) })
	case "fetch":
		if len(args) != 2 {
			return errors.New("distro fetch: need <name>")
		}
		return withApp(func(a *app.App) error {
			// The CLI stays blocking (the REST fetch is the async one): a
			// percentage on stderr, redrawn only when it changes, so piping
			// the printed path stays clean.
			last := -1
			path, err := a.EnsureDistro(args[1], func(done, total int64) {
				if total <= 0 {
					return
				}
				if pct := int(done * 100 / total); pct != last {
					last = pct
					fmt.Fprintf(os.Stderr, "\rdownloading %s… %d%%", args[1], pct)
				}
			})
			if last >= 0 {
				fmt.Fprintln(os.Stderr)
			}
			if err != nil {
				return err
			}
			fmt.Println(path)
			return nil
		})
	case "update":
		check := false
		rest := args[1:]
		if len(rest) > 0 && rest[0] == "--check" {
			check = true
			rest = rest[1:]
		}
		if len(rest) != 1 {
			return errors.New("distro update: need [--check] <name>")
		}
		name := rest[0]
		return withApp(func(a *app.App) error {
			if check {
				current, latest, err := a.CheckDistroUpdate(name)
				if err != nil {
					return err
				}
				if current == latest {
					fmt.Printf("%s is already the newest release (%s)\n", name, current)
				} else {
					fmt.Printf("%s %s → %s available (run `compy distro update %s`)\n", name, current, latest, name)
				}
				return nil
			}
			last := -1
			current, latest, updated, err := a.UpdateDistro(name, func(done, total int64) {
				if total <= 0 {
					return
				}
				if pct := int(done * 100 / total); pct != last {
					last = pct
					fmt.Fprintf(os.Stderr, "\rdownloading %s… %d%%", name, pct)
				}
			})
			if last >= 0 {
				fmt.Fprintln(os.Stderr)
			}
			if err != nil {
				return err
			}
			if !updated {
				fmt.Printf("%s is already the newest release (%s)\n", name, current)
			} else {
				fmt.Printf("%s updated %s → %s\n", name, current, latest)
			}
			return nil
		})
	default:
		return fmt.Errorf("distro: unknown subcommand %q", args[0])
	}
}

func cmdService(args []string) error {
	if len(args) != 1 {
		return errors.New("service: need install|uninstall|status")
	}
	switch args[0] {
	case "install":
		return withApp(func(a *app.App) error { return a.Apply() })
	case "uninstall":
		return launchd.Uninstall()
	case "status":
		running, err := launchd.Running()
		if err != nil {
			fmt.Println("not installed")
			return nil
		}
		fmt.Println(map[bool]string{true: "running", false: "stopped"}[running])
		return nil
	default:
		return fmt.Errorf("service: unknown subcommand %q", args[0])
	}
}

func cmdEnv(args []string) error {
	if len(args) > 0 && (args[0] == "set-os" || args[0] == "unset-os") {
		return withApp(func(a *app.App) error { return a.SetOSEnv(args[0] == "set-os") })
	}
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	shell := fs.String("shell", "sh", "sh|fish|pwsh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withApp(func(a *app.App) error {
		vars, err := a.Vars()
		if err != nil {
			return err
		}
		script, err := envvars.Script(vars, *shell)
		if err != nil {
			return err
		}
		fmt.Print(script)
		return nil
	})
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	lines := fs.Int("lines", 50, "number of lines to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withApp(func(a *app.App) error {
		tail, err := a.Log(*lines)
		if err != nil {
			return err
		}
		fmt.Print(tail)
		return nil
	})
}

func cmdRun(args []string) error {
	argv := args
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return errors.New("run: need a command")
	}
	return withApp(func(a *app.App) error {
		vars, err := a.Vars()
		if err != nil {
			return err
		}
		code, err := envvars.Run(vars, argv)
		if err != nil {
			return err
		}
		os.Exit(code)
		return nil
	})
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	port := fs.Int("port", 0, "port (0 = pick a free one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withApp(func(a *app.App) error {
		// Listen ourselves rather than let ServeListener's server bind:
		// with --port 0 we need the chosen port to print (and open) the URL.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
		if err != nil {
			return err
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
		fmt.Println(url)
		if runtime.GOOS == "darwin" {
			_ = exec.Command("open", url).Run()
		}
		return webui.ServeListener(ln, a.WebUIAPI())
	})
}

// editSource opens a COPY of the template source in $EDITOR, then runs the
// edited text through the save pipeline (render, validate-or-restore). A
// copy, not the live file: the pipeline's nothing-was-saved promise means a
// rejected save leaves the stored source untouched, which editing in place
// would break before the save even ran.
func editSource(a *app.App, name string) error {
	src, ok, err := a.ConfigSource(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("config %q has no template source to edit (plain yaml: use `compy config edit`)", name)
	}
	tmp, err := os.CreateTemp("", "compy-"+name+"-*.tmpl")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return editThen(tmp.Name(), func(s string) error {
		_, err := a.WriteConfigSource(name, s, true)
		return err
	})
}

// editThen opens path in $EDITOR (vi by default), then hands the edited
// content to save (which decides whether a re-activation is needed).
func editThen(path string, save func(string) error) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return save(string(content))
}
