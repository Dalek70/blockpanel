// Package update checks GitHub releases for a newer BlockPanel build and can
// download it and swap the running binary in place ("self-update").
//
// Trust model: updates come exclusively from the configured GitHub repository
// over HTTPS (api.github.com plus GitHub's asset CDN). When the release
// carries a SHA256SUMS asset, the downloaded file must match it. The new
// binary is also executed with --version as a sanity check before it replaces
// the current one. There is no additional signature scheme — enabling
// auto-update means trusting the GitHub account that owns the repository.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"blockpanel/internal/version"
)

// Repo is the GitHub repository releases are published to.
const Repo = "Dalek70/blockpanel"

const (
	apiLatest      = "https://api.github.com/repos/" + Repo + "/releases/latest"
	maxAssetSize   = 200 << 20 // refuse absurd downloads
	requestTimeout = 30 * time.Second
	checkEvery     = 6 * time.Hour
)

// Release describes the newest published release.
type Release struct {
	Version     string    `json:"version"` // normalized, no leading v
	TagName     string    `json:"tag_name"`
	URL         string    `json:"url"` // human release page
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`

	assets []asset
}

type asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Status is what the API reports to admins.
type Status struct {
	Current         string    `json:"current"`
	Latest          string    `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at,omitzero"`
	CheckError      string    `json:"check_error,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	Applying        bool      `json:"applying"`
	// ApplyError is the outcome of the most recent failed apply, cleared
	// when a new apply starts. The UI polls for it after "Update now".
	ApplyError string `json:"apply_error,omitempty"`
}

// Manager caches check results and serializes applies.
type Manager struct {
	// PrepareRestart is called right before the process is replaced —
	// the panel uses it to stop running Minecraft servers gracefully.
	PrepareRestart func()
	// AutoEnabled reports whether unattended updates are on.
	AutoEnabled func() bool
	// Audit records an audit-log entry (action, detail).
	Audit func(action, detail string)

	client *http.Client

	mu       sync.Mutex
	latest   *Release
	checked  time.Time
	checkErr string
	applying bool
	applyErr string
}

func NewManager() *Manager {
	return &Manager{client: &http.Client{Timeout: requestTimeout}}
}

// Status returns the cached view; it never blocks on the network.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Current: version.Current, CheckedAt: m.checked, CheckError: m.checkErr, Applying: m.applying, ApplyError: m.applyErr}
	if m.latest != nil {
		st.Latest = m.latest.Version
		st.UpdateAvailable = version.Newer(m.latest.Version, version.Current)
		st.ReleaseURL = m.latest.URL
		if st.UpdateAvailable {
			st.Notes = m.latest.Notes
		}
	}
	return st
}

// Check fetches the latest release from GitHub and caches the result.
func (m *Manager) Check(ctx context.Context) (Status, error) {
	rel, err := m.fetchLatest(ctx)
	m.mu.Lock()
	m.checked = time.Now()
	if err != nil {
		m.checkErr = err.Error()
	} else {
		m.checkErr = ""
		m.latest = rel
	}
	m.mu.Unlock()
	return m.Status(), err
}

func (m *Manager) fetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiLatest, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "blockpanel/"+version.Current)
	res, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
	case 404:
		return nil, errors.New("no releases published yet")
	case 403, 429:
		return nil, errors.New("GitHub API rate limit reached — try again later")
	default:
		return nil, fmt.Errorf("GitHub API: HTTP %d", res.StatusCode)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []asset   `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("GitHub API: bad response: %w", err)
	}
	if body.Draft || body.Prerelease {
		return nil, errors.New("latest release is a draft/pre-release")
	}
	ver := strings.TrimPrefix(strings.TrimPrefix(body.TagName, "v"), "V")
	if !version.Valid(ver) {
		return nil, fmt.Errorf("release tag %q is not a version — expected e.g. v1.2.0", body.TagName)
	}
	notes := body.Body
	if len(notes) > 4000 {
		notes = notes[:4000] + "…"
	}
	return &Release{
		Version: ver, TagName: body.TagName, URL: body.HTMLURL,
		Notes: notes, PublishedAt: body.PublishedAt, assets: body.Assets,
	}, nil
}

// Apply downloads the latest release, verifies it, swaps the binary and
// restarts the process. On success it does not return. On failure the error
// is also kept in Status.ApplyError so the UI can show it.
func (m *Manager) Apply(ctx context.Context) (err error) {
	m.mu.Lock()
	if m.applying {
		m.mu.Unlock()
		return errors.New("an update is already in progress")
	}
	rel := m.latest
	m.applying = true
	m.applyErr = ""
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.applying = false
		if err != nil {
			m.applyErr = err.Error()
		}
		m.mu.Unlock()
	}()

	if rel == nil {
		return errors.New("no release information — check for updates first")
	}
	if !version.Newer(rel.Version, version.Current) {
		return fmt.Errorf("already up to date (v%s)", version.Current)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)

	newBin, cleanup, err := m.download(ctx, rel, dir)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := verifyBinary(newBin, rel.Version); err != nil {
		return err
	}

	// Swap: keep the old binary as .previous for manual rollback, then move
	// the new one into place. Both renames are within one directory.
	prev := exe + ".previous"
	os.Remove(prev)
	if err := os.Rename(exe, prev); err != nil {
		return fmt.Errorf("backing up current binary: %w", err)
	}
	if err := os.Rename(newBin, exe); err != nil {
		// Try to roll back so the panel is still startable.
		if rerr := os.Rename(prev, exe); rerr != nil {
			return fmt.Errorf("installing new binary failed (%v) AND rollback failed (%v) — reinstall manually, backup at %s", err, rerr, prev)
		}
		return fmt.Errorf("installing new binary: %w", err)
	}

	if m.Audit != nil {
		m.Audit("panel.update", "v"+version.Current+" -> v"+rel.Version)
	}
	if m.PrepareRestart != nil {
		m.PrepareRestart()
	}
	// Does not return on success. On failure the new binary is already in
	// place — the panel keeps running the old code until restarted.
	if rerr := restartProcess(exe); rerr != nil {
		return fmt.Errorf("v%s is installed but the automatic restart failed (%v) — restart the panel to finish (servers were already stopped)", rel.Version, rerr)
	}
	return nil
}

// download fetches the right asset into dir and returns the path of the new
// binary file (not yet executable-verified). cleanup removes leftovers.
func (m *Manager) download(ctx context.Context, rel *Release, dir string) (path string, cleanup func(), err error) {
	plat := "blockpanel-" + runtime.GOOS + "-" + runtime.GOARCH
	var binAsset, zipAsset, sumsAsset *asset
	for i := range rel.assets {
		a := &rel.assets[i]
		switch {
		case a.Name == "SHA256SUMS":
			sumsAsset = a
		case strings.HasSuffix(a.Name, ".zip") && strings.HasPrefix(a.Name, "blockpanel"):
			zipAsset = a
		case strings.Contains(a.Name, runtime.GOOS) && strings.Contains(a.Name, runtime.GOARCH) && strings.HasPrefix(a.Name, "blockpanel"):
			binAsset = a
		}
	}
	pick := binAsset
	if pick == nil {
		pick = zipAsset
	}
	if pick == nil {
		return "", nil, fmt.Errorf("release v%s has no asset for %s/%s (looked for %q or a blockpanel*.zip)",
			rel.Version, runtime.GOOS, runtime.GOARCH, plat)
	}
	if pick.Size > maxAssetSize {
		return "", nil, fmt.Errorf("asset %s is unexpectedly large (%d bytes)", pick.Name, pick.Size)
	}

	var sums map[string]string
	if sumsAsset != nil {
		raw, err := m.fetchAsset(ctx, sumsAsset.URL, 1<<20)
		if err != nil {
			return "", nil, fmt.Errorf("downloading SHA256SUMS: %w", err)
		}
		sums = parseSums(raw)
	}

	raw, err := m.fetchAsset(ctx, pick.URL, maxAssetSize)
	if err != nil {
		return "", nil, fmt.Errorf("downloading %s: %w", pick.Name, err)
	}
	if sums != nil {
		want, ok := sums[pick.Name]
		if !ok {
			return "", nil, fmt.Errorf("SHA256SUMS has no entry for %s", pick.Name)
		}
		got := sha256.Sum256(raw)
		if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
			return "", nil, fmt.Errorf("checksum mismatch for %s — refusing to install", pick.Name)
		}
	}

	if pick == zipAsset {
		raw, err = extractFromZip(raw, plat)
		if err != nil {
			return "", nil, err
		}
	}

	f, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return "", nil, err
	}
	tmp := f.Name()
	cleanup = func() { os.Remove(tmp) }
	if _, err := f.Write(raw); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp, cleanup, nil
}

// fetchAsset downloads one release asset, refusing redirects that leave
// GitHub's domains.
func (m *Manager) fetchAsset(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := checkGitHubURL(rawURL); err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return checkGitHubURL(req.URL.String())
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "blockpanel/"+version.Current)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return data, nil
}

func checkGitHubURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-HTTPS download from %s", rawURL)
	}
	host := u.Hostname()
	ok := host == "github.com" || host == "api.github.com" || host == "objects.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if !ok {
		return fmt.Errorf("refusing download from unexpected host %q", host)
	}
	return nil
}

func parseSums(raw []byte) map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && len(fields[0]) == 64 {
			sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
		}
	}
	return sums
}

// extractFromZip pulls the platform binary out of the release zip
// (layout: blockpanel-<version>/bin/blockpanel-<os>-<arch>).
func extractFromZip(raw []byte, wantName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("release zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != wantName || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxAssetSize {
			return nil, errors.New("binary in zip exceeds size limit")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxAssetSize))
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("release zip has no %s", wantName)
}

// verifyBinary runs the downloaded binary with --version and checks it
// identifies as blockpanel at the expected version.
func verifyBinary(path, wantVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("new binary failed to run: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "blockpanel" {
		return fmt.Errorf("new binary does not identify as blockpanel (said %q)", strings.TrimSpace(string(out)))
	}
	if version.Compare(fields[1], wantVersion) != 0 {
		return fmt.Errorf("new binary reports version %s, release says %s", fields[1], wantVersion)
	}
	return nil
}

// cleanupStale removes .update-* temp files left in the executable's
// directory by a crash or shutdown mid-download. Called once at boot, when
// no download can legitimately be in flight.
func cleanupStale() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	stale, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".update-*"))
	for _, f := range stale {
		os.Remove(f)
	}
}

// StartLoop checks periodically and applies updates unattended when
// AutoEnabled reports true. Call once at boot.
func (m *Manager) StartLoop(ctx context.Context, logf func(format string, args ...any)) {
	cleanupStale()
	go func() {
		// First check shortly after boot so the UI has data, then steady.
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			timer.Reset(checkEvery)

			cctx, cancel := context.WithTimeout(ctx, requestTimeout)
			st, err := m.Check(cctx)
			cancel()
			if err != nil {
				logf("update check: %v", err)
				continue
			}
			if !st.UpdateAvailable {
				continue
			}
			if m.AutoEnabled == nil || !m.AutoEnabled() {
				logf("update available: v%s (current v%s) — see Panel Settings", st.Latest, st.Current)
				continue
			}
			logf("auto-update: installing v%s (current v%s)…", st.Latest, st.Current)
			actx, acancel := context.WithTimeout(ctx, 15*time.Minute)
			err = m.Apply(actx)
			acancel()
			if err != nil {
				logf("auto-update failed: %v", err)
			}
		}
	}()
}
