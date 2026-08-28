package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bronto-community/compy/internal/distro"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/version"
)

// download is one collector binary's in-flight (or finished) fetch, as the
// Settings screen renders it: a progress bar while it runs, "download
// failed · <reason>" when it does not.
type download struct {
	status string // "downloading" | "done" | "failed"
	pct    int
	err    string
}

// beginDownload marks name as downloading, reporting false if a fetch for it
// is already in flight (two extracts into one directory is nobody's idea of
// a good time).
func (a *App) beginDownload(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.downloads == nil {
		a.downloads = map[string]download{}
	}
	if a.downloads[name].status == "downloading" {
		return false
	}
	a.downloads[name] = download{status: "downloading"}
	return true
}

// setDownloadProgress records bytes-so-far as a percentage. A server that
// declares no length leaves the bar where it is — an indeterminate strip is
// the UI's problem, not a made-up number's.
func (a *App) setDownloadProgress(name string, done, total int64) {
	if total <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.downloads[name] = download{status: "downloading", pct: int(done * 100 / total)}
}

// endDownload records how the fetch finished.
func (a *App) endDownload(name string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.downloads[name] = download{status: "failed", err: err.Error()}
		return
	}
	a.downloads[name] = download{status: "done", pct: 100}
}

// DownloadProgress reports how name's fetch is going. A name nobody has
// fetched in this process is "idle" — including one that is already on disk,
// which the distro list says separately.
func (a *App) DownloadProgress(name string) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.downloads[name]
	if !ok {
		d = download{status: "idle"}
	}
	out := map[string]any{"status": d.status, "pct": d.pct}
	if d.err != "" {
		out["error"] = d.err
	}
	return out, nil
}

// fetchClient is the client behind every outbound fetch (distro archives,
// the GitHub release listing, .sha256 assets). Deadlines sit on each phase —
// dial, TLS handshake, first response byte — with deliberately no overall
// Timeout: an archive is hundreds of MB and its transfer time is the user's
// bandwidth, but a connection that stalls before answering must not wedge an
// activation's auto-download or the tray's hourly update loop.
var fetchClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}}

// httpFetch is distro.Fetch over plain HTTP(S), reporting Content-Length as
// the total (-1 when the server declares none); the caller closes the body.
func httpFetch(url string) (io.ReadCloser, int64, error) {
	resp, err := fetchClient.Get(url)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// fetchFn returns the injected Fetch, or plain HTTP(S).
func (a *App) fetchFn() distro.Fetch {
	if a.Fetch != nil {
		return a.Fetch
	}
	return httpFetch
}

// EnsureDistro resolves a distro name to a collector binary path: "" means
// the global default from settings (or DefaultDistro when none is set), a
// user-registered entry is used as-is, and a shipped definition is
// downloaded (checksum-verified) on first use, reporting bytes to progress
// (nil = a.Progress, if set) as it arrives. A download is also reported to
// the same in-process tracker the Settings screen polls (DownloadProgress),
// so an automatic fetch during activation shows the same progress bar as an
// explicit one.
func (a *App) EnsureDistro(name string, progress distro.Progress) (string, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return "", err
	}
	if name == "" {
		name = effectiveDistro(s)
	}
	user, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(user, func(d state.Distro) bool { return d.Name == name }); i >= 0 {
		return user[i].Path, nil
	}
	if name == distro.BundledName {
		p, _ := distro.Bundled()
		if p == "" {
			return "", state.BadRequest(errors.New("the bundled collector is not built — run packaging/collector/build.sh"))
		}
		return p, nil
	}
	for _, d := range distro.Defs() {
		if d.Name == name {
			if !distro.Available(d) {
				return "", state.BadRequest(fmt.Errorf("distro %q has no build for this platform", name))
			}
			if progress == nil && a.Progress != nil {
				progress = func(done, total int64) { a.Progress(name, done, total) }
			}
			// Tracker entries begin lazily, on the first byte: an
			// already-installed binary reports nothing. began stays false
			// when another fetch owns the tracker slot (StartFetchDistro),
			// which then also owns endDownload.
			began := false
			track := func(done, total int64) {
				if !began {
					began = a.beginDownload(name)
				}
				a.setDownloadProgress(name, done, total)
				if progress != nil {
					progress(done, total)
				}
			}
			// A first download goes straight to the newest release the
			// persisted check knows — one download, no install-then-update
			// hop. An already-installed binary is returned as-is: switching
			// versions is UpdateDistro's job, never a fetch side effect.
			//
			// Trust model: the compiled-in pin is verified against its
			// compiled-in sha256; any other release is verified against the
			// .sha256 asset published next to its tarball in the same
			// upstream release (TLS, same origin) — the pulled-update path.
			target := distro.EffectiveVersion(d, s)
			if distro.InstalledPath(a.Dir, d, s) == "" {
				if chk, _ := state.LoadUpdateCheck(); distro.NewerVersion(chk.Latest, target) {
					target = chk.Latest
				}
			}
			path, err := distro.EnsureVersion(a.Dir, d, target, a.fetchFn(), track)
			if began {
				a.endDownload(name, err)
			}
			if err == nil && target != distro.EffectiveVersion(d, s) {
				// Record the pulled version so the registry, later
				// resolutions, and update checks all agree on what is
				// installed (fresh load: settings may have moved meanwhile).
				cur, lerr := state.LoadSettings()
				if lerr != nil {
					return "", lerr
				}
				if cur.DistroVersions == nil {
					cur.DistroVersions = map[string]string{}
				}
				cur.DistroVersions[d.Name] = target
				if serr := state.SaveSettings(cur); serr != nil {
					return "", serr
				}
			}
			return path, err
		}
	}
	return "", state.BadRequest(fmt.Errorf("no such distro %q", name))
}

// StartFetchDistro begins downloading name's collector binary and returns
// at once: a download takes seconds and the Settings screen follows it with
// DownloadProgress rather than holding a request open. A fetch already in
// flight for the same name is left alone. Everything that can go wrong —
// including an unknown name — surfaces through the progress, since the
// request that started it is gone by then.
func (a *App) StartFetchDistro(name string) error {
	if !a.beginDownload(name) {
		return nil
	}
	go func() {
		// EnsureDistro reports the bytes into the tracker itself; this
		// goroutine owns the begin/end so pre-download errors (an unknown
		// name) still land in the progress.
		_, err := a.EnsureDistro(name, nil)
		a.endDownload(name, err)
	}()
	return nil
}

// Distros lists the distro registry: shipped definitions (flagged available
// for this platform and whether they are downloaded) plus user entries.
// "user_entry" distinguishes an actual registry override/custom distro (in
// state.LoadDistros/distros.json — DELETE-able) from a shipped definition
// that's merely been downloaded to its default path but never overridden
// (not DELETE-able: there's no registry entry to remove).
func (a *App) Distros() ([]map[string]any, error) {
	reg, err := distro.Registry(a.Dir)
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	userDistros, err := state.LoadDistros()
	if err != nil {
		return nil, err
	}
	isUserEntry := make(map[string]bool, len(userDistros))
	for _, u := range userDistros {
		isUserEntry[u.Name] = true
	}
	defs := map[string]distro.Def{}
	for _, d := range distro.Defs() {
		defs[d.Name] = d
	}
	// Best-effort: an unreadable check-result cache means no availability
	// claim, never a broken listing (the zero value claims nothing).
	chk, _ := state.LoadUpdateCheck()
	selected := effectiveDistro(s)
	out := make([]map[string]any, 0, len(reg))
	for _, d := range reg {
		def, isDef := defs[d.Name]
		bundled := d.Name == distro.BundledName && !isUserEntry[d.Name]
		version := ""
		if isDef {
			version = distro.EffectiveVersion(def, s)
		} else if bundled && d.Path != "" {
			_, version = distro.Bundled()
		}
		row := map[string]any{
			"name":       d.Name,
			"path":       d.Path,
			"version":    version,
			"selected":   d.Name == selected,
			"definition": isDef,
			"bundled":    bundled,
			"available":  !isDef || distro.Available(def),
			"downloaded": d.Path != "",
			"user_entry": isUserEntry[d.Name],
		}
		// An update claim rides only on INSTALLED updatable rows — pinned
		// definitions, downloaded, not overridden by a user entry. The
		// bundled collector updates with compy, user paths are the user's to
		// update, and an undownloaded row has nothing to update: it instead
		// advertises what a download would fetch (fetch_version — the
		// persisted latest when a check has run, else the compiled-in pin,
		// flagged fetch_pinned so the UI can say which it is). NewerVersion
		// never claims on a malformed/unknown version on either side.
		if isDef && !isUserEntry[d.Name] {
			if !chk.CheckedAt.IsZero() {
				row["checked_at"] = chk.CheckedAt.Format(time.RFC3339)
			}
			if d.Path != "" {
				if distro.NewerVersion(chk.Latest, version) {
					row["latest_available"] = chk.Latest
				}
			} else {
				row["version"] = "" // nothing installed — no version claim
				if distro.Available(def) {
					fv := version
					if chk.Latest == "" {
						row["fetch_pinned"] = true
					} else if distro.NewerVersion(chk.Latest, fv) {
						fv = chk.Latest
					}
					row["fetch_version"] = fv
				}
			}
		}
		// A download this process started without the settings screen's
		// help (activation auto-fetching the default) rides along in the
		// row, so the screen's periodic refresh still shows the bar.
		a.mu.Lock()
		if dl, ok := a.downloads[d.Name]; ok && dl.status == "downloading" {
			row["download"] = map[string]any{"status": dl.status, "pct": dl.pct}
		}
		a.mu.Unlock()
		out = append(out, row)
	}
	return out, nil
}

// distroOverrideWarning returns the warning text AddDistro prints when name
// collides with a shipped distro definition, or "" if it doesn't.
func distroOverrideWarning(name string) string {
	if slices.ContainsFunc(distro.Defs(), func(d distro.Def) bool { return d.Name == name }) {
		return fmt.Sprintf("%q is a shipped distro definition; this path overrides it", name)
	}
	return ""
}

// AddDistroWarning reports the warning AddDistro would print for name (the
// shipped-definition-override text), or "" if none applies. It lets the
// REST surface return the same warning as a response field instead of only
// a stderr line.
func (a *App) AddDistroWarning(name string) string { return distroOverrideWarning(name) }

// validateDistroBinary resolves path to an absolute path and checks it
// exists and is executable.
func validateDistroBinary(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", abs)
	}
	return abs, nil
}

// selectDistroIfNone makes name the global default distro if none is preset
// yet (first registration).
func selectDistroIfNone(name string) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.Distro != "" {
		return nil
	}
	s.Distro = name
	return state.SaveSettings(s)
}

// AddDistro registers a collector binary, selecting it if it is the first.
func (a *App) AddDistro(name, path string) error {
	if !state.ValidBackendName(name) {
		return state.BadRequest(fmt.Errorf("invalid distro name %q: use lowercase letters, digits, dashes", name))
	}
	abs, err := validateDistroBinary(path)
	if err != nil {
		return state.BadRequest(err)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(distros, func(d state.Distro) bool { return d.Name == name }) {
		return state.BadRequest(fmt.Errorf("distro %q already exists", name))
	}
	if w := distroOverrideWarning(name); w != "" {
		fmt.Fprintf(os.Stderr, "compy: %s\n", w)
	}
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return err
	}
	return selectDistroIfNone(name)
}

// SetDistroPath registers or updates a user distro registry entry's binary
// path (must exist and be executable), selecting it as the default if none
// is set yet. Overriding a shipped definition's name returns the same
// warning AddDistro's stderr line carries, as a response field instead.
func (a *App) SetDistroPath(name, path string) (string, error) {
	if !state.ValidBackendName(name) {
		return "", state.BadRequest(fmt.Errorf("invalid distro name %q: use lowercase letters, digits, dashes", name))
	}
	abs, err := validateDistroBinary(path)
	if err != nil {
		return "", state.BadRequest(err)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	warning := distroOverrideWarning(name)
	if i := slices.IndexFunc(distros, func(d state.Distro) bool { return d.Name == name }); i >= 0 {
		distros[i].Path = abs
		return warning, state.SaveDistros(distros)
	}
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return "", err
	}
	return warning, selectDistroIfNone(name)
}

// RemoveDistro removes a user registry entry. Removing a definition-name
// override "reverts" to the shipped definition (still selectable, and
// downloads on next use); removing an entry with no shipped definition
// drops it from the registry entirely — the response's "reverted" field
// says which happened. It returns a state.BadRequest-marked error (400) for
// a pure definition name with no user entry (nothing to remove) or for the
// selected distro (pick another default first).
func (a *App) RemoveDistro(name string) (bool, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return false, err
	}
	if s.Distro == name {
		return false, state.BadRequest(fmt.Errorf("distro %q is the selected default; select another distro first", name))
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(distros, func(d state.Distro) bool { return d.Name == name })
	if i < 0 {
		return false, state.BadRequest(fmt.Errorf("no user distro entry named %q", name))
	}
	distros = slices.Delete(distros, i, i+1)
	if err := state.SaveDistros(distros); err != nil {
		return false, err
	}
	reverted := slices.ContainsFunc(distro.Defs(), func(d distro.Def) bool { return d.Name == name })
	return reverted, nil
}

// UseDistro selects the global default distro, re-applying if a
// configuration is active. The switch sticks when the collector starts, and
// when the active configuration is merely rejected by it (nothing moved); it
// does not stick when the new collector fails to start, because the restore
// that puts the previous configuration back restores settings.json with it.
func (a *App) UseDistro(name string) error {
	if _, err := a.EnsureDistro(name, nil); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	s.Distro = name
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if s.ActiveConfig == "" {
		return nil
	}
	err = a.Apply()
	if err == nil {
		return nil
	}
	// Whether the switch survived depends on how the apply failed, so say
	// which happened rather than assume.
	//
	// A configuration the collector rejects never reached launchd: nothing
	// moved, the switch stands, and it is a caller mistake (400).
	if state.IsBadRequest(err) {
		// Already marked, so the wrap stays a 400 (IsBadRequest unwraps).
		return fmt.Errorf("default collector is now %q, but the active configuration does not run with it: %w", name, err)
	}
	// A collector that will not start restores the last-good snapshot,
	// settings.json included, which puts the previous collector back — so
	// read the setting rather than claim it stuck.
	if after, lerr := state.LoadSettings(); lerr == nil && after.Distro != name {
		return fmt.Errorf("%q did not start; the collector is still %q: %w", name, effectiveDistro(after), err)
	}
	// Anything else — a plist write, a launchctl refusal — is our fault and
	// keeps both its own message and its 500 (the collector log tail the UI
	// shows there is the diagnostic).
	return err
}

// updatableDef returns the shipped definition behind name when `compy
// distro update` applies to it. The bundled collector, user-managed
// entries, and undownloaded definitions (nothing installed to update — a
// download fetches the newest release directly) are refused with a message
// the UI shows verbatim.
func (a *App) updatableDef(name string) (distro.Def, error) {
	if name == distro.BundledName {
		return distro.Def{}, state.BadRequest(errors.New("the bundled collector updates with compy releases"))
	}
	user, err := state.LoadDistros()
	if err != nil {
		return distro.Def{}, err
	}
	if slices.ContainsFunc(user, func(d state.Distro) bool { return d.Name == name }) {
		return distro.Def{}, state.BadRequest(fmt.Errorf("distro %q is user-managed — update the binary at its path yourself", name))
	}
	for _, d := range distro.Defs() {
		if d.Name == name {
			if !distro.Available(d) {
				return distro.Def{}, state.BadRequest(fmt.Errorf("distro %q has no build for this platform", name))
			}
			s, err := state.LoadSettings()
			if err != nil {
				return distro.Def{}, err
			}
			if distro.InstalledPath(a.Dir, d, s) == "" {
				return distro.Def{}, state.BadRequest(fmt.Errorf("distro %q is not downloaded — a download fetches the newest release directly", name))
			}
			return d, nil
		}
	}
	return distro.Def{}, state.BadRequest(fmt.Errorf("no such distro %q", name))
}

// latestVersion is the one release-check path — the on-demand check and the
// tray's background MaybeCheckUpdates both land here, so every successful
// check records the same distro-updates.json and all surfaces agree. One
// listing covers every pinned distro. A record-write failure doesn't fail
// the check (the caller still gets its answer); it just isn't cached.
func (a *App) latestVersion() (string, error) {
	latest, err := distro.LatestVersion(a.fetchFn())
	if err != nil {
		// GitHub's failure, not the caller's and not ours: 502 on the REST
		// surface, and the web UI shows it without a collector log tail.
		return "", state.Upstream(err)
	}
	// Load-modify-save: the compy half of the record (CompyLatest) belongs
	// to its own independent check and must survive this one.
	chk, _ := state.LoadUpdateCheck()
	chk.Latest = latest
	chk.CheckedAt = time.Now().UTC()
	if err := state.SaveUpdateCheck(chk); err != nil {
		fmt.Fprintln(os.Stderr, "compy: record release check:", err)
	}
	return latest, nil
}

// compyReleaseAPI is compy's own latest-release lookup; a var so tests can
// point the injected fetch at a stub. While the repo is private the
// unauthenticated call 404s — checkCompyUpdate's failure is silent by
// design and self-resolves when the repo goes public.
var compyReleaseAPI = "https://api.github.com/repos/bronto-community/compy/releases/latest"

// checkCompyUpdate records compy's own newest release beside the collector
// result. Written independently of latestVersion's fields: either half
// failing leaves the other's record intact.
func (a *App) checkCompyUpdate() error {
	rc, _, err := a.fetchFn()(compyReleaseAPI)
	if err != nil {
		return err
	}
	defer rc.Close()
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&r); err != nil {
		return fmt.Errorf("compy release check: %w", err)
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if v == "" {
		return errors.New("compy release check: no tag_name in the response")
	}
	chk, _ := state.LoadUpdateCheck()
	chk.CompyLatest = v
	return state.SaveUpdateCheck(chk)
}

// CompyUpdateAvailable reports compy's newest known release when it is
// strictly newer than this build — release builds only: a dev build never
// claims (its version line already says dev, and semver against a commit is
// meaningless). Read-only like UpdateAvailable: a file read, never network.
func (a *App) CompyUpdateAvailable() string {
	chk, err := state.LoadUpdateCheck()
	if err != nil {
		return ""
	}
	return compyUpdateFrom(chk.CompyLatest, version.Release())
}

// compyUpdateFrom is the claiming rule: only a release build (non-empty
// releaseVersion) with a strictly newer known release claims anything.
// NewerVersion's malformed-refuses-to-claim rule guards both sides.
func compyUpdateFrom(latestKnown, releaseVersion string) string {
	if releaseVersion == "" || !distro.NewerVersion(latestKnown, releaseVersion) {
		return ""
	}
	return latestKnown
}

// updateCheckInterval is the background release-check cadence. The tray's
// goroutine calls MaybeCheckUpdates more often; it declines until due.
const updateCheckInterval = 12 * time.Hour

// MaybeCheckUpdates runs one upstream release check when the persisted
// result is older than updateCheckInterval — the tray's background job.
// Fully silent on failure (offline, rate-limited): stderr at most, the
// previous result stands with its honest checked_at, the next call retries.
func (a *App) MaybeCheckUpdates() {
	chk, err := state.LoadUpdateCheck()
	if err == nil && time.Since(chk.CheckedAt) < updateCheckInterval {
		return
	}
	if _, err := a.latestVersion(); err != nil {
		fmt.Fprintln(os.Stderr, "compy: release check:", err)
	}
	// compy's own release: independent of the collector half — its 404
	// (private repo, unauthenticated) must not poison the record above.
	if err := a.checkCompyUpdate(); err != nil {
		fmt.Fprintln(os.Stderr, "compy: compy release check:", err)
	}
}

// UpdateAvailable reports the newest known upstream release when it is
// newer than some updatable pinned distro's version in effect, "" when none
// is. Read-only — it answers from the persisted check result, never the
// network — so the tray can afford it on its 5s resync.
func (a *App) UpdateAvailable() (string, error) {
	chk, err := state.LoadUpdateCheck()
	if err != nil || chk.Latest == "" {
		return "", err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return "", err
	}
	user, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	for _, d := range distro.Defs() {
		if slices.ContainsFunc(user, func(u state.Distro) bool { return u.Name == d.Name }) {
			continue // user-managed override: theirs to update
		}
		// Only an INSTALLED distro can be outdated: an undownloaded one has
		// nothing to update (its first download fetches the latest directly).
		if distro.InstalledPath(a.Dir, d, s) == "" {
			continue
		}
		if distro.NewerVersion(chk.Latest, distro.EffectiveVersion(d, s)) {
			return chk.Latest, nil
		}
	}
	return "", nil
}

// checkDistroUpdate resolves name's definition, its version in effect, and
// the latest upstream release (persisting the check result). A network
// failure or rate limit surfaces as its own error, never as a claim about
// versions.
func (a *App) checkDistroUpdate(name string) (d distro.Def, current, latest string, err error) {
	d, err = a.updatableDef(name)
	if err != nil {
		return d, "", "", err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return d, "", "", err
	}
	current = distro.EffectiveVersion(d, s)
	latest, err = a.latestVersion()
	return d, current, latest, err
}

// CheckDistroUpdate reports name's version in effect and the latest
// upstream release, downloading nothing.
func (a *App) CheckDistroUpdate(name string) (current, latest string, err error) {
	_, current, latest, err = a.checkDistroUpdate(name)
	return current, latest, err
}

// applyDistroUpdate downloads and verifies version alongside the current
// one (versioned dirs — nothing is overwritten), records it as d's version
// in effect, and — when d is the collector in use and launchd reports the
// job running — re-applies so the running collector switches to it. A new
// collector that fails to start rolls the whole update back: distro
// versions live in settings.json, which the last-good restore puts back
// with the rest of the setup. A stopped collector stays stopped.
func (a *App) applyDistroUpdate(d distro.Def, version string, progress distro.Progress) error {
	name := d.Name
	if progress == nil && a.Progress != nil {
		progress = func(done, total int64) { a.Progress(name, done, total) }
	}
	began := false
	track := func(done, total int64) {
		if !began {
			began = a.beginDownload(name)
		}
		a.setDownloadProgress(name, done, total)
		if progress != nil {
			progress(done, total)
		}
	}
	_, err := distro.EnsureVersion(a.Dir, d, version, a.fetchFn(), track)
	if began {
		a.endDownload(name, err)
	}
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.DistroVersions == nil {
		s.DistroVersions = map[string]string{}
	}
	s.DistroVersions[name] = version
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if effectiveDistro(s) != name || s.ActiveConfig == "" {
		return nil
	}
	if running, rerr := launchd.Running(); rerr != nil || !running {
		return nil // stopped stays stopped; the new version runs on next start
	}
	err = a.Apply()
	if err == nil {
		return nil
	}
	// Same honesty as UseDistro: whether the update survived depends on how
	// the apply failed. A rejected configuration never reached launchd (the
	// old binary keeps running until the next restart, which uses version);
	// a collector that would not start was rolled back by the last-good
	// restore, settings.json — version record included — with it.
	if state.IsBadRequest(err) {
		return fmt.Errorf("%s is now %s, but the active configuration does not run with it: %w", name, version, err)
	}
	if after, lerr := state.LoadSettings(); lerr == nil && after.DistroVersions[name] != version {
		return fmt.Errorf("%s %s did not start; the collector is still %s: %w", name, version, distro.EffectiveVersion(d, after), err)
	}
	return err
}

// UpdateDistro is the blocking update the CLI uses: check upstream, and
// when a newer release exists, download, verify, and switch to it.
// updated=false with err=nil means name is already the newest release.
func (a *App) UpdateDistro(name string, progress distro.Progress) (current, latest string, updated bool, err error) {
	d, current, latest, err := a.checkDistroUpdate(name)
	// Strictly newer only — an equal or OLDER upstream answer is "already
	// newest", never a silent downgrade (NewerVersion also refuses any
	// malformed version outright).
	if err != nil || !distro.NewerVersion(latest, current) {
		return current, latest, false, err
	}
	return current, latest, true, a.applyDistroUpdate(d, latest, progress)
}

// StartUpdateDistro is UpdateDistro for the REST surface: the release check
// answers synchronously (started=false with latest==current means nothing
// to do), the download runs in the background reporting to the same tracker
// the fetch progress route polls. An update already in flight is left alone.
func (a *App) StartUpdateDistro(name string) (current, latest string, started bool, err error) {
	d, current, latest, err := a.checkDistroUpdate(name)
	if err != nil {
		return "", "", false, err
	}
	if !distro.NewerVersion(latest, current) {
		return current, latest, false, nil // equal/older/malformed: never a downgrade
	}
	if !a.beginDownload(name) {
		return current, latest, true, nil
	}
	go func() {
		err := a.applyDistroUpdate(d, latest, nil)
		a.endDownload(name, err)
	}()
	return current, latest, true, nil
}
