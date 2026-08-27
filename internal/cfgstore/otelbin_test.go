package cfgstore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The owner's real share link, as otelbin's server answered it on 2026-08-27:
// GET https://www.otelbin.io/s/180fda380204bfa1f1891415c50bfe0323efbb01
// → 307, Location: https://www.otelbin.io/?#config=<otelbinExampleEncoded>
const otelbinExampleEncoded = `**H_Learn_more_about_the_OpenTelemetry_Collector_via*N*H_https%3A%2F%2Fopentelemetry.io%2Fdocs%2Fcollector%2F*N*Nreceivers%3A*N__otlp%3A*N____protocols%3A*N______grpc%3A*N______http%3A*N*Nprocessors%3A*N__batch%3A*N*Nexporters%3A*N__otlp%3A*N____endpoint%3A_otelcol%3A4317*N*Nextensions%3A*N__health*_check%3A*N__pprof%3A*N__zpages%3A*N*Nservice%3A*N__extensions%3A_%5Bhealth*_check%2C_pprof%2C_zpages%5D*N__pipelines%3A*N____traces%3A*N______receivers%3A_%5Botlp%5D*N______processors%3A_%5Bbatch%5D*N______exporters%3A_%5Botlp%5D*N____metrics%3A*N______receivers%3A_%5Botlp%5D*N______processors%3A_%5Bbatch%5D*N______exporters%3A_%5Botlp%5D*N____logs%3A*N______receivers%3A_%5Botlp%5D*N______processors%3A_%5Bbatch%5D*N______exporters%3A_%5Botlp%5D%7E`

const otelbinExampleYAML = `# Learn more about the OpenTelemetry Collector via
# https://opentelemetry.io/docs/collector/

receivers:
  otlp:
    protocols:
      grpc:
      http:

processors:
  batch:

exporters:
  otlp:
    endpoint: otelcol:4317

extensions:
  health_check:
  pprof:
  zpages:

service:
  extensions: [health_check, pprof, zpages]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]`

func TestOTelBinFragmentDecode(t *testing.T) {
	got, err := decodeOTelBinFragment("config=" + otelbinExampleEncoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != otelbinExampleYAML {
		t.Errorf("decoded yaml mismatch:\n%s", got)
	}
}

func TestOTelBinFragmentDecodeErrors(t *testing.T) {
	for _, frag := range []string{
		"",                 // no config param
		"view=pipeline",    // other params only
		"config=_T~",       // a boolean, not a string
		"config=123~",      // a number, not a string
		"config=*",         // truncated escape
		"config=abc*Xdef~", // unknown escape code
		"config=%GG",       // unreadable percent-encoding
		"config=**~",       // empty string
	} {
		if _, err := decodeOTelBinFragment(frag); err == nil {
			t.Errorf("fragment %q: want error, got none", frag)
		}
	}
}

func TestIsOTelBinURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.otelbin.io/#config=abc~":        true,
		"https://otelbin.io/#config=abc~":            true,
		"https://www.otelbin.io/s/180fda380204bfa1":  true,
		"https://OTELBIN.IO/s/abc":                   true,
		"https://raw.githubusercontent.com/x/y.yaml": false,
		"https://example.com/#config=abc~":           false,
		"https://notmyotelbin.io/s/abc":              false,
		"not a url at all ::":                        false,
	} {
		if got := IsOTelBinURL(raw); got != want {
			t.Errorf("IsOTelBinURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

// Short-link resolution against a stub shaped like the real server's answer:
// a 307 whose Location is a fragment-form URL.
func TestOTelBinShortLinkResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/abc123" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "/?#config="+otelbinExampleEncoded)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	got, err := otelbinYAML(client, srv.URL+"/s/abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != otelbinExampleYAML {
		t.Errorf("resolved yaml mismatch:\n%s", got)
	}

	// A shape change (200 HTML page, no redirect) must fail clearly.
	if _, err := otelbinYAML(client, srv.URL+"/s/gone"); err == nil ||
		!strings.Contains(err.Error(), "does not understand") {
		t.Errorf("shape change: want 'does not understand' error, got %v", err)
	}
}

// A fragment-form otelbin URL through the normal create path lands as a
// plain LOCAL config — no network, no remote_url, no pristine hash.
func TestCreateFromOTelBinFragmentURL(t *testing.T) {
	root := t.TempDir()
	fetch := func(url string) ([]byte, error) {
		t.Fatalf("plain fetch must not run for otelbin URL %q", url)
		return nil, nil
	}
	url := "https://www.otelbin.io/#config=" + otelbinExampleEncoded
	if err := CreateFromURL(root, "imported", url, fetch); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "imported")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != otelbinExampleYAML {
		t.Errorf("stored yaml mismatch:\n%s", yaml)
	}
	if info.Provenance != "local" {
		t.Errorf("provenance = %q, want local", info.Provenance)
	}
	if info.Meta.RemoteURL != "" || info.Meta.PristineSHA256 != "" {
		t.Errorf("otelbin config must not be syncable: %+v", info.Meta)
	}
}
