package cfgstore

import "embed"

// embeddedDefaults holds the shipped default configurations (debug.yaml for
// now; T6 adds otlp.yaml and bronto.yaml).
//
//go:embed defaults/*.yaml
var embeddedDefaults embed.FS
