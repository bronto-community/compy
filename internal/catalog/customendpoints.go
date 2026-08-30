package catalog

import "strings"

// This file is the Go half of the render vocabulary: everything the boring
// rule keeps out of template bodies. It computes flat values from whichever
// recognized knobs a schema declares (the custom-endpoints shapes:
// "backends" rows, the pipeline toggles) — with safe lookups throughout, so
// a user template that declares only some of them still renders, the rest
// zero-valued. The shipped custom-endpoints body only ranges over what is
// computed here; user templates may draw on the same names.

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
// It MUST agree with SecretEnv's naming for the row's api_key field — the
// rendered ${env:} references and the activation environment share it.
func envVarFor(name string) string {
	return secretEnvName(name, "api_key")
}

// vocabulary computes the render data from normalized knobs:
// needs_delta/needs_cumulative, the temporality-split metrics groups, the
// canonical processor order, and the per-signal exporter lists (debug tee
// included). Every lookup is safe: a schema that doesn't declare a knob gets
// the zero (or the shipped default, for a backend row's shape fields), so
// any template — user-authored included — can use as much or as little of
// the vocabulary as it declares knobs for.
func vocabulary(norm map[string]any, storageDir string) map[string]any {
	knob := func(k string) bool { b, _ := norm[k].(bool); return b }
	d := ceData{
		MemoryLimiter:     knob("memory_limiter"),
		Batch:             knob("batch"),
		ResourceDetection: knob("resource_detection"),
		OfflineQueue:      knob("offline_queue"),
		DebugTee:          knob("debug_tee"),
		StorageDir:        storageDir,
	}

	type be struct {
		data        ceBackend
		signals     map[string]bool
		temporality string
	}
	var backends []be
	rows, _ := norm["backends"].([]any)
	for _, r := range rows {
		row, _ := r.(map[string]any)
		name, _ := row["name"].(string)
		endpoint, _ := row["endpoint"].(string)
		scheme, _ := row["auth_scheme"].(string)
		prefix := ""
		if scheme != "" && scheme != "none" {
			prefix = scheme + " "
		}
		temporality, _ := row["temporality"].(string)
		if temporality == "" {
			temporality = "as-is"
		}
		signals, ok := row["signals"].([]string)
		if !ok {
			signals = []string{"traces", "metrics", "logs"}
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
				Endpoint:    endpoint,
				AuthHeader:  authHeader,
				AuthPrefix:  prefix,
				EnvVar:      envVarFor(name),
				ExtraHeader: extraHeader,
				ExtraValue:  extraValue,
				HasHeaders:  authHeader != "" || extraHeader != "",
			},
			signals:     map[string]bool{},
			temporality: temporality,
		}
		for _, s := range signals {
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
	return map[string]any{
		"Backends":          d.Backends,
		"MemoryLimiter":     d.MemoryLimiter,
		"Batch":             d.Batch,
		"ResourceDetection": d.ResourceDetection,
		"OfflineQueue":      d.OfflineQueue,
		"DebugTee":          d.DebugTee,
		"NeedsDelta":        d.NeedsDelta,
		"NeedsCumulative":   d.NeedsCumulative,
		"AnyProcs":          d.AnyProcs,
		"HasTraces":         d.HasTraces,
		"HasLogs":           d.HasLogs,
		"TracesProcs":       d.TracesProcs,
		"TracesExps":        d.TracesExps,
		"LogsProcs":         d.LogsProcs,
		"LogsExps":          d.LogsExps,
		"MetricsGroups":     d.MetricsGroups,
		"StorageDir":        d.StorageDir,
	}
}
