package app

import (
	"fmt"

	"github.com/bronto-community/compy/internal/webui"
)

// configDetail is the web UI's view of one configuration: its Info plus
// YAML, plus the template source for a tier-3 config.
func (a *App) configDetail(name string) (any, error) {
	info, yaml, err := a.Config(name)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"info": info, "yaml": yaml}
	if src, ok, _ := a.ConfigSource(name); ok {
		detail["source"] = src
	}
	return detail, nil
}

// settingsMap is the web UI's view of Settings.
func (a *App) settingsMap() (map[string]any, error) {
	s, err := a.GetSettings()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"grpc_port": s.GRPCPort,
		"http_port": s.HTTPPort,
		"protocol":  s.EffectiveProtocol(),
	}, nil
}

// statusMap is the web UI's view of Status (plus the OTLP endpoint it
// displays).
func (a *App) statusMap() (map[string]any, error) {
	st, err := a.Status()
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"running":   st.Running,
		"distro":    st.Distro,
		"grpc_port": st.GRPCPort,
		"http_port": st.HTTPPort,
		"protocol":  st.Protocol,
		"endpoint":  fmt.Sprintf("http://127.0.0.1:%d", st.EndpointPort()),
		"config":    st.Config,
		"preset":    st.Preset,
		"os_env":    st.OSEnv,
		"recent":    st.Recent,
		"listening": st.Listening,

		"compy_version": st.CompyVersion,
	}
	if st.CompyUpdate != "" {
		m["compy_update"] = st.CompyUpdate
	}
	if st.StaleBinary {
		m["stale_binary"] = true
	}
	if st.Conformance != nil {
		m["conformance"] = st.Conformance
	}
	return m, nil
}

// WebUIAPI wires the web UI's closures onto App methods: the full v2 REST
// surface (docs/superpowers/plans/2026-08-25-compy-v2-p2-rest.md).
func (a *App) WebUIAPI() webui.API {
	return webui.API{
		Status:   a.statusMap,
		Log:      a.Log,
		Env:      a.EnvInfo,
		SetOSEnv: a.SetOSEnv,

		GetSettings: a.settingsMap,
		PutSettings: a.PutSettings,
		AdoptPorts:  a.AdoptPorts,

		Health:       a.Health,
		Apply:        a.Apply,
		Stop:         a.Stop,
		Start:        a.Start,
		Validate:     a.Validate,
		FactoryReset: a.FactoryReset,

		Configs:           func() (any, error) { return a.Configs() },
		CreateConfig:      a.CreateConfig,
		CreateFromURL:     a.CreateFromURL,
		Templates:         func() (any, error) { return a.Templates() },
		CreateFromCatalog: a.CreateFromCatalog,
		PutConfigSource: func(name, source string, knobs map[string]any) error {
			_, err := a.WriteConfigSource(name, source, knobs, true)
			return err
		},
		PutConfigSourceNoValidate: func(name, source string, knobs map[string]any) (bool, error) {
			return a.WriteConfigSource(name, source, knobs, false)
		},
		GetConfig:               a.configDetail,
		PutConfigYAML:           a.WriteConfigYAML,
		PutConfigYAMLNoValidate: a.WriteConfigYAMLNoValidate,
		PutConfigMeta:           a.UpdateConfigMeta,
		DeleteConfig:            a.DeleteConfig,
		CopyConfig:              a.CopyConfig,
		Activate:                a.Activate,
		ValidateConfig:          a.ValidateConfig,
		Sync:                    a.Sync,
		Resync:                  a.Resync,
		Reset:                   a.Reset,
		RenameConfig:            a.RenameConfig,
		SyncAll:                 a.SyncAll,

		PutPreset:    a.ReplacePreset,
		DeletePreset: a.DeletePreset,
		UsePreset:    a.UsePreset,
		RenamePreset: a.RenamePreset,

		Distros: func() (any, error) { return a.Distros() },
		AddDistro: func(name, path string) (string, error) {
			warning := a.AddDistroWarning(name)
			if err := a.AddDistro(name, path); err != nil {
				return "", err
			}
			return warning, nil
		},
		SetDistroPath:     a.SetDistroPath,
		RemoveDistro:      a.RemoveDistro,
		UseDistro:         a.UseDistro,
		FetchDistro:       a.StartFetchDistro,
		DownloadProgress:  a.DownloadProgress,
		CheckDistroUpdate: a.CheckDistroUpdate,
		UpdateDistro:      a.StartUpdateDistro,
	}
}
