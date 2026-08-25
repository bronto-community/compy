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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/envvars"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/tray"
	"github.com/bronto-io/compy/internal/webui"
	"github.com/bronto-io/compy/internal/window"
)

const usage = `compy — local OpenTelemetry Collector manager

  compy status [--json]
  compy apply | validate | stop | start
  compy config list
  compy config show|edit|delete|sync|resync <name>
  compy config create <name> [--from-url URL]
  compy config copy <src> <dst>
  compy config sync-all
  compy config set-url <config> <url|-->
  compy use <config> [<preset>]
  compy vars <config>
  compy presets set <config> <preset> KEY=VALUE
  compy presets use|delete <config> <preset>
  compy presets rename <config> <from> <to>
  compy settings
  compy settings set [--grpc-port N] [--http-port N]
  compy distro list
  compy distro add <name> <path>
  compy distro set-path <name> <path>
  compy distro remove <name>
  compy distro use|fetch <name>
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
		fmt.Fprintln(os.Stderr, "compy: "+err.Error())
		os.Exit(1)
	}
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
	case "apply":
		return withApp(func(a *app.App) error { return a.Apply() })
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
	case "use":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New("use: need <config> [<preset>]")
		}
		preset := ""
		if len(rest) == 2 {
			preset = rest[1]
		}
		return withApp(func(a *app.App) error { return a.Activate(rest[0], preset) })
	case "vars":
		if len(rest) != 1 {
			return errors.New("vars: need <config>")
		}
		return withApp(func(a *app.App) error { return printVars(a, rest[0]) })
	case "presets":
		return cmdPresets(rest)
	case "settings":
		return cmdSettings(rest)
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
// quitting from the menu sticks until next login or reinstall.
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
	return fn(a)
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
		fmt.Printf("service:  %s\nconfig:   %s\ndistro:   %s\nendpoint: http://127.0.0.1:%d (grpc %d)\n",
			running, config, distro, st.HTTPPort, st.GRPCPort)
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
		fromURL := fs.String("from-url", "", "fetch the configuration from this URL")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		return withApp(func(a *app.App) error {
			if *fromURL != "" {
				return a.CreateFromURL(name, *fromURL)
			}
			return a.CreateConfig(name, blankConfig)
		})
	case "copy":
		if len(rest) != 2 {
			return errors.New("config copy: need <src> <dst>")
		}
		return withApp(func(a *app.App) error { return a.CopyConfig(rest[0], rest[1]) })
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
	case "show", "edit", "delete", "sync", "resync":
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
			case "delete":
				return a.DeleteConfig(name)
			case "sync":
				return a.Sync(name)
			case "resync":
				return a.Resync(name)
			default:
				if !state.ValidBackendName(name) {
					return fmt.Errorf("invalid config name %q", name)
				}
				return editThen(a.ConfigPath(name),
					func(s string) error { return a.WriteConfigYAML(name, s) })
			}
		})
	default:
		return fmt.Errorf("config: unknown subcommand %q", sub)
	}
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

// printVars renders the configuration's variables as a table: one row per
// variable, one column per preset.
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
			fmt.Fprintf(w, "\t%s", info.Meta.Presets[preset][v.Name])
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
			return withApp(func(a *app.App) error { return a.SetVar(args[1], args[2], key, value) })
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
			fmt.Printf("grpc-port: %d\nhttp-port: %d\n", s.GRPCPort, s.HTTPPort)
			return nil
		})
	}
	if args[0] != "set" {
		return fmt.Errorf("settings: unknown subcommand %q", args[0])
	}
	fs := flag.NewFlagSet("settings set", flag.ContinueOnError)
	var grpcPort, httpPort int
	fs.IntVar(&grpcPort, "grpc-port", 0, "gRPC port")
	fs.IntVar(&httpPort, "http-port", 0, "HTTP port")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var grpcP, httpP *int
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "grpc-port":
			grpcP = &grpcPort
		case "http-port":
			httpP = &httpPort
		}
	})
	return withApp(func(a *app.App) error { return a.PutSettings(grpcP, httpP) })
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
				note := "user"
				if d["definition"] == true {
					switch {
					case d["available"] != true:
						note = "unavailable on this platform"
					case d["downloaded"] == true:
						note = "downloaded"
					default:
						note = "available (downloads on first use)"
					}
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
		// net.Listen rather than webui.Serve: with --port 0 we need the
		// chosen port to print (and open) the URL.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
		if err != nil {
			return err
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
		fmt.Println(url)
		if runtime.GOOS == "darwin" {
			_ = exec.Command("open", url).Run()
		}
		return http.Serve(ln, webui.Handler(a.WebUIAPI()))
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
