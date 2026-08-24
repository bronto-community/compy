// Command compy manages a local OpenTelemetry Collector: backends, distros,
// the LaunchAgent service, environment variables, and a small web UI.
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
	"runtime"
	"strings"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/config"
	"github.com/bronto-io/compy/internal/envvars"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/webui"
)

const usage = `compy — local OpenTelemetry Collector manager

  compy status [--json]
  compy apply | rollback | validate
  compy backend list
  compy backend add <name> --kind otlp-grpc|otlp-http|bronto|debug [--endpoint URL] [--api-key KEY]
  compy backend remove|enable|disable|edit <name>
  compy distro list
  compy distro add <name> <path>
  compy distro use <name>
  compy service install|uninstall|status
  compy env [--shell sh|fish|pwsh]
  compy env set-os | unset-os
  compy run -- <cmd...>
  compy raw on|off|edit
  compy ui [--port N]
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
	case "rollback":
		return withApp(func(a *app.App) error { return a.Rollback() })
	case "validate":
		return withApp(func(a *app.App) error {
			if err := a.Validate(); err != nil {
				return err
			}
			fmt.Println("config ok")
			return nil
		})
	case "backend":
		return cmdBackend(rest)
	case "distro":
		return cmdDistro(rest)
	case "service":
		return cmdService(rest)
	case "env":
		return cmdEnv(rest)
	case "run":
		return cmdRun(rest)
	case "raw":
		return cmdRaw(rest)
	case "ui":
		return cmdUI(rest)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
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
		distro := st.Distro
		if distro == "" {
			distro = "(none)"
		}
		fmt.Printf("service:  %s\ndistro:   %s\nendpoint: http://127.0.0.1:%d (grpc %d)\nenabled:  %s\nraw mode: %v\n",
			running, distro, st.HTTPPort, st.GRPCPort, strings.Join(st.Enabled, ", "), st.RawMode)
		return nil
	})
}

func cmdBackend(args []string) error {
	if len(args) == 0 {
		return errors.New("backend: need a subcommand")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return withApp(func(a *app.App) error {
			backends, err := a.Backends()
			if err != nil {
				return err
			}
			for _, b := range backends {
				mark := " "
				if b["enabled"] == true {
					mark = "*"
				}
				fmt.Printf("%s %s\n", mark, b["name"])
			}
			return nil
		})
	case "add":
		if len(rest) == 0 {
			return errors.New("backend add: need a name")
		}
		name := rest[0]
		fs := flag.NewFlagSet("backend add", flag.ContinueOnError)
		kind := fs.String("kind", "", "otlp-grpc|otlp-http|bronto|debug")
		endpoint := fs.String("endpoint", "", "exporter endpoint URL")
		apiKey := fs.String("api-key", "", "API key")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		return withApp(func(a *app.App) error { return a.AddBackend(name, *kind, *endpoint, *apiKey) })
	case "remove", "enable", "disable", "edit":
		if len(rest) != 1 {
			return fmt.Errorf("backend %s: need exactly one name", sub)
		}
		name := rest[0]
		return withApp(func(a *app.App) error {
			switch sub {
			case "remove":
				return a.RemoveBackend(name)
			case "enable":
				return a.SetEnabled(name, true)
			case "disable":
				return a.SetEnabled(name, false)
			default:
				return editThen(config.BackendPath(a.Dir, name),
					func(s string) error { return a.WriteFragment(name, s) })
			}
		})
	default:
		return fmt.Errorf("backend: unknown subcommand %q", sub)
	}
}

func cmdDistro(args []string) error {
	if len(args) == 0 {
		return errors.New("distro: need a subcommand")
	}
	switch args[0] {
	case "list":
		distros, err := state.LoadDistros()
		if err != nil {
			return err
		}
		s, err := state.LoadSettings()
		if err != nil {
			return err
		}
		for _, d := range distros {
			mark := " "
			if d.Name == s.Distro {
				mark = "*"
			}
			fmt.Printf("%s %s\t%s\n", mark, d.Name, d.Path)
		}
		return nil
	case "add":
		if len(args) != 3 {
			return errors.New("distro add: need <name> <path>")
		}
		return withApp(func(a *app.App) error { return a.AddDistro(args[1], args[2]) })
	case "use":
		if len(args) != 2 {
			return errors.New("distro use: need <name>")
		}
		return withApp(func(a *app.App) error { return a.UseDistro(args[1]) })
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

func cmdRaw(args []string) error {
	if len(args) != 1 {
		return errors.New("raw: need on|off|edit")
	}
	return withApp(func(a *app.App) error {
		switch args[0] {
		case "on":
			return a.SetRawMode(true)
		case "off":
			return a.SetRawMode(false)
		case "edit":
			return editThen(a.RawPath(), a.WriteRaw)
		default:
			return fmt.Errorf("raw: unknown subcommand %q", args[0])
		}
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
// content to save (which decides whether an apply is needed).
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
