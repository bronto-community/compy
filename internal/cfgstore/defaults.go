package cfgstore

import "embed"

// embeddedDefaults holds the shipped default configurations: debug, otlp,
// bronto.
//
//go:embed defaults/*.yaml
var embeddedDefaults embed.FS
