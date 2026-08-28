// otelbin.io share links: both forms carry a whole collector config.
//
//   - fragment form: https://www.otelbin.io/#config=<encoded> — the config
//     is in the URL fragment, encoded with jsurl2 (otelbin vendors
//     github.com/wmertens/jsurl2 in
//     packages/otelbin/src/lib/urlState/jsurl2.ts) and then packed into a
//     URLSearchParams string.
//   - short form: https://www.otelbin.io/s/<id> — the server answers with a
//     redirect whose Location is a fragment-form URL.
//
// A link is a snapshot, not a syncable source: decoding one produces a
// plain LOCAL configuration (no remote_url, no pristine hash).
package cfgstore

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IsOTelBinURL reports whether raw is an otelbin.io share link (either form).
func IsOTelBinURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return isOTelBinHost(u.Hostname())
}

func isOTelBinHost(h string) bool {
	h = strings.ToLower(h)
	return h == "otelbin.io" || h == "www.otelbin.io"
}

// FetchOTelBinYAML turns an otelbin.io share link into the collector YAML it
// carries. Fragment-form links decode locally; short links cost one HTTP
// round trip to read the redirect.
func FetchOTelBinYAML(raw string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		// Read redirects ourselves: the config lives in the Location
		// header's fragment, which a followed redirect would not return.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return otelbinYAML(client, raw)
}

func otelbinYAML(client *http.Client, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid otelbin link: %w", err)
	}
	start := strings.ToLower(u.Hostname())
	for range 5 {
		if frag := u.EscapedFragment(); frag != "" {
			return decodeOTelBinFragment(frag)
		}
		resp, err := client.Get(u.String())
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
			return "", fmt.Errorf("otelbin returned something compy does not understand (HTTP %d with no share link)", resp.StatusCode)
		}
		if u, err = u.Parse(loc); err != nil {
			return "", fmt.Errorf("otelbin returned something compy does not understand (bad redirect: %v)", err)
		}
		// Every hop must satisfy the same host check as the initial link
		// (start covers the tests' stub server; live, start IS otelbin):
		// a hostile otelbin must not be able to bounce compy into blind
		// GETs at arbitrary — including internal — hosts.
		if h := strings.ToLower(u.Hostname()); !isOTelBinHost(h) && h != start {
			return "", fmt.Errorf("otelbin returned something compy does not understand (redirect to %s)", u.Hostname())
		}
	}
	return "", errors.New("otelbin returned something compy does not understand (too many redirects)")
}

func decodeOTelBinFragment(frag string) (string, error) {
	vals, err := url.ParseQuery(frag)
	if err != nil {
		return "", fmt.Errorf("otelbin returned something compy does not understand (unreadable fragment: %v)", err)
	}
	enc := vals.Get("config")
	if enc == "" {
		return "", errors.New("otelbin link carries no config")
	}
	yaml, err := jsurl2DecodeString(enc)
	if err != nil || yaml == "" {
		return "", fmt.Errorf("otelbin returned something compy does not understand (%v)", err)
	}
	return yaml, nil
}

// jsurl2From is jsurl2's fromEscape table, verbatim from otelbin's vendored
// copy (jsurl2.ts). `_` outside an escape is a space; `*X` maps through this.
var jsurl2From = map[byte]string{
	'*': "*", '_': "_", '-': "~", 'S': "$", 'P': "+", '"': "'",
	'C': "(", 'D': ")", 'L': "<", 'G': ">", '.': "%", 'Q': "?",
	'H': "#", 'A': "&", 'E': "=", 'B': "\\", 'N': "\n", 'R': "\r",
	'U': "\u2028", 'V': "\u2029", 'Z': "\x00",
}

// jsurl2DecodeString decodes a jsurl2 value that must be a string (otelbin's
// config binding always is). The token runs to the `~` terminator; a leading
// `*` marks a string that does not start with a letter.
func jsurl2DecodeString(s string) (string, error) {
	body, _, _ := strings.Cut(s, "~")
	switch {
	case strings.HasPrefix(body, "*"):
		body = body[1:]
	case body != "" && (body[0] >= 'a' && body[0] <= 'z' || body[0] >= 'A' && body[0] <= 'Z'):
		// plain string token, no prefix
	default:
		return "", errors.New("config is not a string")
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '_':
			b.WriteByte(' ')
		case '*':
			i++
			if i == len(body) {
				return "", errors.New("truncated escape at end of config")
			}
			rep, ok := jsurl2From[body[i]]
			if !ok {
				return "", fmt.Errorf("unknown escape *%c in config", body[i])
			}
			b.WriteString(rep)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}
