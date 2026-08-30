package catalog

import "strings"

// This file is the Go half of the custom-endpoints template: everything the
// boring rule keeps out of the body. The body only ranges over what is
// computed here.

// ceBackend is one backend row, ready to render.
type ceBackend struct {
	Name       string
	Endpoint   string
	AuthHeader string
	AuthPrefix string // "Bearer " etc., "" for scheme none
	EnvVar     string // HONEYCOMB_API_KEY — dashes become underscores
	ExtraHeader,
	ExtraValue string
	HasHeaders bool
}

// ceMetricsGroup is one temporality-split metrics pipeline.
type ceMetricsGroup struct {
	Pipeline string // metrics | metrics/delta | metrics/cumulative
	Procs    string // joined processor list, "" if none
	Exps     string // joined exporter list
}

// ceData is the flat value set the template body sees.
type ceData struct {
	Backends []ceBackend
	MemoryLimiter, Batch, ResourceDetection,
	OfflineQueue, DebugTee bool
	NeedsDelta, NeedsCumulative, AnyProcs bool
	HasTraces, HasLogs                    bool
	TracesProcs, TracesExps               string
	LogsProcs, LogsExps                   string
	MetricsGroups                         []ceMetricsGroup
	StorageDir                            string
}

// envVarFor derives the secret's env var name from a backend name:
// "my-backend" → "MY_BACKEND_API_KEY" (dashes are not legal in env names).
func envVarFor(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
}

// customEndpointsData computes the render data from normalized knobs:
// needs_delta/needs_cumulative, the temporality-split metrics groups, the
// canonical processor order, and the per-signal exporter lists (debug tee
// included).
func customEndpointsData(norm map[string]any, storageDir string) ceData {
	d := ceData{
		MemoryLimiter:     norm["memory_limiter"].(bool),
		Batch:             norm["batch"].(bool),
		ResourceDetection: norm["resource_detection"].(bool),
		OfflineQueue:      norm["offline_queue"].(bool),
		DebugTee:          norm["debug_tee"].(bool),
		StorageDir:        storageDir,
	}

	type be struct {
		data        ceBackend
		signals     map[string]bool
		temporality string
	}
	var backends []be
	for _, r := range norm["backends"].([]any) {
		row := r.(map[string]any)
		name := row["name"].(string)
		scheme := row["auth_scheme"].(string)
		prefix := ""
		if scheme != "none" {
			prefix = scheme + " "
		}
		authHeader, _ := row["auth_header"].(string)
		extraHeader, _ := row["extra_header"].(string)
		extraValue, _ := row["extra_value"].(string)
		if extraHeader == "" {
			extraValue = "" // a value without a header renders nothing
		}
		b := be{
			data: ceBackend{
				Name:        name,
				Endpoint:    row["endpoint"].(string),
				AuthHeader:  authHeader,
				AuthPrefix:  prefix,
				EnvVar:      envVarFor(name),
				ExtraHeader: extraHeader,
				ExtraValue:  extraValue,
				HasHeaders:  authHeader != "" || extraHeader != "",
			},
			signals:     map[string]bool{},
			temporality: row["temporality"].(string),
		}
		for _, s := range row["signals"].([]string) {
			b.signals[s] = true
		}
		backends = append(backends, b)
		d.Backends = append(d.Backends, b.data)
		if b.signals["metrics"] {
			switch b.temporality {
			case "to-delta":
				d.NeedsDelta = true
			case "to-cumulative":
				d.NeedsCumulative = true
			}
		}
	}

	// procs builds the canonical processor order from the toggles:
	// memory_limiter first, enrichment next, conversion before batch, batch
	// last. extra go between detection and batch (the metrics groups'
	// temporality converters).
	procs := func(extra ...string) string {
		var p []string
		if d.MemoryLimiter {
			p = append(p, "memory_limiter")
		}
		if d.ResourceDetection {
			p = append(p, "resource_detection")
		}
		p = append(p, extra...)
		if d.Batch {
			p = append(p, "batch")
		}
		return strings.Join(p, ", ")
	}
	// exps lists the exporters carrying one signal, plus the debug tee.
	exps := func(signal, temporality string) string {
		var e []string
		for _, b := range backends {
			if !b.signals[signal] {
				continue
			}
			if temporality != "" && b.temporality != temporality {
				continue
			}
			e = append(e, "otlphttp/"+b.data.Name)
		}
		if len(e) > 0 && d.DebugTee {
			e = append(e, "debug")
		}
		return strings.Join(e, ", ")
	}

	d.TracesExps = exps("traces", "")
	d.LogsExps = exps("logs", "")
	d.HasTraces = d.TracesExps != ""
	d.HasLogs = d.LogsExps != ""
	d.TracesProcs = procs()
	d.LogsProcs = procs()

	// Metrics pipelines split by temporality, deterministic order.
	for _, g := range []struct {
		temporality, pipeline, converter string
	}{
		{"as-is", "metrics", ""},
		{"to-delta", "metrics/delta", "cumulative_to_delta"},
		{"to-cumulative", "metrics/cumulative", "delta_to_cumulative"},
	} {
		e := exps("metrics", g.temporality)
		if e == "" {
			continue
		}
		var conv []string
		if g.converter != "" {
			conv = append(conv, g.converter)
		}
		d.MetricsGroups = append(d.MetricsGroups, ceMetricsGroup{
			Pipeline: g.pipeline,
			Procs:    procs(conv...),
			Exps:     e,
		})
	}

	d.AnyProcs = d.MemoryLimiter || d.ResourceDetection || d.Batch ||
		d.NeedsDelta || d.NeedsCumulative
	return d
}
