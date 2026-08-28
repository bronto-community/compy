package app

// compy's own update notice: the claiming rule (release builds only) and
// the background check's two independent halves — the compy lookup 404s
// while the repo is private, and that must never poison the collector half.

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/state"
)

func TestCompyUpdateFrom(t *testing.T) {
	cases := []struct {
		name, latest, release, want string
	}{
		{"dev build never claims", "0.2.0", "", ""},
		{"release build, newer known", "0.2.0", "0.1.0", "0.2.0"},
		{"release build, same version", "0.1.0", "0.1.0", ""},
		{"release build, older known", "0.1.0", "0.2.0", ""},
		{"nothing known", "", "0.1.0", ""},
		{"malformed latest never claims", "not-a-version", "0.1.0", ""},
		{"malformed release never claims", "0.2.0", "dev · abc123", ""},
	}
	for _, c := range cases {
		if got := compyUpdateFrom(c.latest, c.release); got != c.want {
			t.Errorf("%s: compyUpdateFrom(%q, %q) = %q, want %q", c.name, c.latest, c.release, got, c.want)
		}
	}
}

// stubFetch answers the collector listing and compy's latest-release lookup
// independently; "" for either means that half fails.
func stubFetch(collectorLatest, compyLatest string) func(string) (io.ReadCloser, int64, error) {
	return func(url string) (io.ReadCloser, int64, error) {
		if strings.Contains(url, "bronto-community/compy") {
			if compyLatest == "" {
				return nil, 0, fmt.Errorf("fetch %s: HTTP 404", url)
			}
			body := fmt.Sprintf(`{"tag_name":"v%s"}`, compyLatest)
			return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
		}
		if collectorLatest == "" {
			return nil, 0, fmt.Errorf("fetch %s: HTTP 500", url)
		}
		body := fmt.Sprintf(`[{"tag_name":"v%s","prerelease":false}]`, collectorLatest)
		return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
	}
}

// The private-repo reality: compy's unauthenticated lookup 404s. Silent
// no-claim, and the collector half still records its result.
func TestMaybeCheckUpdatesCompy404LeavesCollectorHalf(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	a := &App{Fetch: stubFetch("0.161.0", "")}
	a.MaybeCheckUpdates()
	chk, err := state.LoadUpdateCheck()
	if err != nil {
		t.Fatal(err)
	}
	if chk.Latest != "0.161.0" || chk.CheckedAt.IsZero() {
		t.Fatalf("collector half poisoned by the compy 404: %+v", chk)
	}
	if chk.CompyLatest != "" {
		t.Fatalf("a failed compy lookup must not claim: %+v", chk)
	}
}

// Both halves succeed: both recorded in one file, the collector's overwrite
// preserving the compy field's independence and vice versa.
func TestMaybeCheckUpdatesRecordsBothHalves(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	// A stale previous record (due for a re-check) with old values in both
	// halves — both must be replaced, not merged oddly or dropped.
	if err := state.SaveUpdateCheck(state.UpdateCheck{
		Latest: "0.150.0", CompyLatest: "0.1.0",
		CheckedAt: time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{Fetch: stubFetch("0.161.0", "0.2.0")}
	a.MaybeCheckUpdates()
	chk, err := state.LoadUpdateCheck()
	if err != nil {
		t.Fatal(err)
	}
	if chk.Latest != "0.161.0" || chk.CompyLatest != "0.2.0" {
		t.Fatalf("got %+v, want latest 0.161.0 and compy 0.2.0", chk)
	}
}

// The reverse independence: the collector listing fails, compy's half still
// records — and the collector's previous record stands untouched.
func TestMaybeCheckUpdatesCollectorFailureLeavesCompyHalf(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	if err := state.SaveUpdateCheck(state.UpdateCheck{
		Latest: "0.150.0", CheckedAt: time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{Fetch: stubFetch("", "0.2.0")}
	a.MaybeCheckUpdates()
	chk, err := state.LoadUpdateCheck()
	if err != nil {
		t.Fatal(err)
	}
	if chk.CompyLatest != "0.2.0" {
		t.Fatalf("compy half poisoned by the collector failure: %+v", chk)
	}
	if chk.Latest != "0.150.0" {
		t.Fatalf("collector's previous record lost: %+v", chk)
	}
}
