package cfgstore

import "embed"

// embeddedDefaults holds the plain shipped default configurations
// (otlp-basic); the templated ones (debug, otlp-forward, bronto) ship as
// catalog templates and materialize from there.
//
//go:embed defaults/*.yaml
var embeddedDefaults embed.FS
