package config

import (
	"bytes"
	"fmt"
	"text/template"
)

// presetData is the template input for a rendered backend fragment. Name
// is the backend name, keeping exporter component IDs unique per backend
// even when two backends share a kind.
type presetData struct {
	Name     string
	Endpoint string
	APIKey   string
}

var (
	otlpGRPCTemplate = template.Must(template.New("otlp-grpc").Parse(otlpGRPCYAML))
	otlpHTTPTemplate = template.Must(template.New("otlp-http").Parse(otlpHTTPYAML))
	debugTemplate    = template.Must(template.New("debug").Parse(debugYAML))
	brontoTemplate   = template.Must(template.New("bronto").Parse(brontoYAML))
)

const otlpGRPCYAML = `exporters:
  otlp/{{.Name}}:
    endpoint: {{.Endpoint}}
{{- if .APIKey}}
    headers:
      x-api-key: {{.APIKey}}
{{- end}}
service:
  pipelines:
    traces:
      exporters: [otlp/{{.Name}}]
    metrics:
      exporters: [otlp/{{.Name}}]
    logs:
      exporters: [otlp/{{.Name}}]
`

const otlpHTTPYAML = `exporters:
  otlphttp/{{.Name}}:
    endpoint: {{.Endpoint}}
{{- if .APIKey}}
    headers:
      x-api-key: {{.APIKey}}
{{- end}}
service:
  pipelines:
    traces:
      exporters: [otlphttp/{{.Name}}]
    metrics:
      exporters: [otlphttp/{{.Name}}]
    logs:
      exporters: [otlphttp/{{.Name}}]
`

const debugYAML = `exporters:
  debug/{{.Name}}:
    verbosity: normal
service:
  pipelines:
    traces:
      exporters: [debug/{{.Name}}]
    metrics:
      exporters: [debug/{{.Name}}]
    logs:
      exporters: [debug/{{.Name}}]
`

// bronto forwards to Bronto's OTLP ingestion endpoint per the bronto skill
// docs (https://ingestion.<region>.bronto.io, auth header X-BRONTO-API-KEY).
const brontoYAML = `exporters:
  otlphttp/{{.Name}}:
    endpoint: {{.Endpoint}}
{{- if .APIKey}}
    headers:
      X-BRONTO-API-KEY: {{.APIKey}}
{{- end}}
service:
  pipelines:
    traces:
      exporters: [otlphttp/{{.Name}}]
    metrics:
      exporters: [otlphttp/{{.Name}}]
    logs:
      exporters: [otlphttp/{{.Name}}]
`

// Preset renders a backend config fragment for the given kind and backend
// name.
func Preset(kind, name, endpoint, apiKey string) ([]byte, error) {
	tmpl, ok := map[string]*template.Template{
		"otlp-grpc": otlpGRPCTemplate,
		"otlp-http": otlpHTTPTemplate,
		"debug":     debugTemplate,
		"bronto":    brontoTemplate,
	}[kind]
	if !ok {
		return nil, fmt.Errorf("unknown preset kind %q", kind)
	}
	data := presetData{Name: name, Endpoint: endpoint, APIKey: apiKey}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
