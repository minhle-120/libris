// Package engine owns the llama.cpp binaries libris runs.
//
// "Engine" is the word for them because they are not a model and not a server: they are
// the program libris starts in order to have a server at all. A fresh install has none, and
// the whole point of this package is that a person does not have to know what a GGUF or a
// release asset is to get from that to an app that answers.
//
// Nothing is pinned. Upstream moves every day, ships the binaries on nightly b#### tags
// rather than on the semver tag (v1.0.0 carries one text file and no binaries), and its
// Vulkan build is the one asset that runs on NVIDIA, AMD and Intel alike on both operating
// systems libris ships for. So the app asks what is current, offers an update when its copy
// is older, and verifies the bytes it fetches against the digest GitHub publishes — which
// is the only reason an unpinned download is safe to exec at all.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Backend selects which prebuilt build to fetch. Vulkan is the default and the only one
// offered by choice: it is the single asset that works on NVIDIA, AMD and Intel on both
// operating systems, which spares the user a driver question they cannot answer correctly.
type Backend string

const (
	Vulkan Backend = "vulkan"
	CPU    Backend = "cpu"
)

// Repository is upstream: api/releases are the two endpoints used. There is deliberately no
// mirror setting — a prefix on the file download with no prefix on the version query is two
// sources of truth about which bytes are current, and the digest is checked against the one
// that answered the API.
const (
	owner      = "ggml-org"
	repo       = "llama.cpp"
	apiBase    = "https://api.github.com/repos/" + owner + "/" + repo + "/releases"
	releaseDir = "https://github.com/" + owner + "/" + repo + "/releases/download"

	// nightlyAge is how long a "what is current" answer stays worth reusing. Longer and
	// the settings page asks GitHub on every visit; shorter and a day of opening the app
	// is a day of unauthenticated API calls, which are limited to sixty an hour per
	// address on a shared connection.
	nightlyAge = 24 * time.Hour
)

// tagPattern is a nightly: b followed by digits. The semver tags are real releases and
// carry no binaries, so they must not be picked just because they sort higher.
var tagPattern = regexp.MustCompile(`^b\d{4,6}$`)

// ErrBusy means a download is already running. Two at once would fight over the same
// .parts file and produce a file that verifies against neither.
// releaseTimeout bounds the update check, which the settings page waits on. Long enough for
// a slow but working connection, short enough that a hung one is an error sentence rather
// than a page that never finishes loading.
// releaseTimeout bounds the update check. Generous for a few kilobytes, and deliberately: a
// measured api.github.com round trip on a real network here is seven seconds, so a tighter
// bound would turn a slow connection into a page that says the newest build is unknown. It
// exists only so a socket that accepts and never answers cannot hold the settings page open
// forever.
const releaseTimeout = 15 * time.Second

var ErrBusy = errors.New("engine: a download is already running")

// ErrNotFound means the release exists but has nothing usable for this platform — which
// happens: upstream renames assets, and a nightly occasionally drops a build.
var ErrNotFound = errors.New("engine: no prebuilt build for this platform in that release")

// Release is one upstream nightly, as far as this package cares.
type Release struct {
	Tag       string    `json:"tag"`
	Published time.Time `json:"published"`
	URL       string    `json:"url,omitempty"`
}

// Asset is a build for one platform, with the digest GitHub publishes for it.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
	// SHA256 is the digest without its "sha256:" prefix. Empty means GitHub published no
	// digest, and Install refuses rather than running an unverified binary.
	SHA256   string `json:"sha256,omitempty"`
	Resolved bool   `json:"-"`
}

// Installed is the copy on this disk.
type Installed struct {
	Tag         string    `json:"tag"`
	Backend     Backend   `json:"backend"`
	Path        string    `json:"path"`
	Bytes       int64     `json:"bytes"`
	InstalledAt time.Time `json:"installed_at"`
	SHA256      string    `json:"sha256,omitempty"`
	// Upstream is the tag that was newest when this was installed, which is what makes
	// "Update available" a fact rather than a guess.
	Upstream string `json:"upstream,omitempty"`
}

// State is where the one job is.
type State string

const (
	StateIdle        State = "idle"
	StateDownloading State = "downloading"
	StateVerifying   State = "verifying"
	StateUnpacking   State = "unpacking"
	StateDone        State = "done"
	StateFailed      State = "failed"
)

// Progress is what the settings page polls. It is a snapshot rather than a stream because
// the page already polls a job that way — a source that is `processing` is refetched, and a
// progress bar that reconnects itself to an event stream is a second way to be out of date.
type Progress struct {
	State   State  `json:"state"`
	Tag     string `json:"tag,omitempty"`
	Asset   string `json:"asset,omitempty"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
	Since   string `json:"since,omitempty"`
	Resumed bool   `json:"resumed,omitempty"`
}

// Manager is the package's whole interface to the rest of libris. It holds no
// configuration: the data directory is given once, and the settings that change what it
// does — the backend — is an argument, because this package must not import config any
// more than llm does.
type Manager struct {
	dir string // <data>/bin

	// api and files are the two upstream endpoints. They are fields rather than uses of the
	// constants below so the download path can be exercised against a test server: a
	// package that can only fetch from GitHub can only be tested by GitHub.
	api   string
	files string
	log   *slog.Logger
	http  *http.Client
	// release is the bounded client: a few KB of JSON the settings page waits on, not the
	// 35 MB file the caller's context owns. See Open.
	release *http.Client

	mu       sync.Mutex
	progress Progress
	cancel   context.CancelFunc

	now func() time.Time
}

func Open(dir string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		dir: dir,
		api: apiBase,
		log: log,
		// No Client.Timeout: a 35 MB download over a slow link is a long request, and a
		// client-wide deadline would kill the one thing it is waiting for. The caller's
		// context is the only timeout here — the same reasoning internal/llm gives for
		// never setting one on a streaming request.
		http: &http.Client{},
		// The opposite case, so the opposite setting: the release list is a few KB and the
		// settings page waits on it. Without a bound, an api.github.com that accepts the
		// connection and says nothing holds the whole section open indefinitely — installed
		// tag, storage path and all — on a machine that is otherwise answering fine.
		release:  &http.Client{Timeout: releaseTimeout},
		progress: Progress{State: StateIdle},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Known answers what a check would return from disk, without asking anyone: the release and
// asset last seen for this backend, and whether that answer is still inside its age.
//
// The settings page needs this to be cheap. A page that fetches the newest nightly on every
// visit is a page whose load time is somebody else's server, and this is the call that lets
// it say what it knows immediately and ask in the background.
func (m *Manager) Known(b Backend) (Release, Asset, bool) {
	if b == "" {
		b = Vulkan
	}
	return m.cached(b)
}

func (m *Manager) Dir() string { return m.dir }

// UpstreamPage is where a person can look at the release themselves — the settings page
// links it next to a "what am I about to run" question.
func (r Release) Page() string {
	if r.URL != "" {
		return r.URL
	}
	return releaseDir + "/" + r.Tag
}

// ---- reading --------------------------------------------------------------------------

// Status is the installed copy, if any. A directory named for a nightly but without an
// installed.json is not installed: it is a download that died, and reporting it as an
// engine would let libris try to exec a half-extracted tree.
func (m *Manager) Status() (Installed, bool) {
	raw, err := os.ReadFile(m.indexPath())
	if err != nil {
		return Installed{}, false
	}
	var in Installed
	if err := json.Unmarshal(raw, &in); err != nil || in.Tag == "" {
		return Installed{}, false
	}
	// The directory the record points at has to exist. Someone may have deleted it by hand
	// from the Documents folder, which is a thing a person is allowed to do to their own
	// Documents folder.
	if fi, err := os.Stat(filepath.Join(m.dir, in.Tag)); err != nil || !fi.IsDir() {
		return Installed{}, false
	}
	return in, true
}

// Check asks upstream what the newest nightly with a build for this platform is. The answer
// is cached, with the API's own ETag, so the settings page does not spend the machine's
// hourly unauthenticated allowance every time it is opened.
func (m *Manager) Check(ctx context.Context, b Backend, force bool) (Release, Asset, error) {
	if b == "" {
		b = Vulkan
	}
	if !force {
		if rel, a, ok := m.cached(b); ok {
			return rel, a, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.api+"?per_page=15", nil)
	if err != nil {
		return Release{}, Asset{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag := m.cachedETag(); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	res, err := m.release.Do(req)
	if err != nil {
		// Offline is a normal condition for a local-first app, and a cached answer is
		// better than no answer: "newest I saw on the 21st" beats an error on a page whose
		// only other content is the local version.
		if rel, a, ok := m.cached(b); ok {
			m.log.Debug("engine: update check failed, using the cached answer", "err", err)
			return rel, a, nil
		}
		return Release{}, Asset{}, fmt.Errorf("engine: ask %s for releases: %w", m.api, err)
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusNotModified:
		rel, a, ok := m.cached(b)
		if !ok {
			return Release{}, Asset{}, fmt.Errorf("engine: %s said nothing changed, but the cache is gone", owner+"/"+repo)
		}
		return rel, a, nil
	case http.StatusOK:
	default:
		return Release{}, Asset{}, fmt.Errorf("engine: %s answered %s", m.api, res.Status)
	}

	var page []upstreamRelease
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return Release{}, Asset{}, fmt.Errorf("engine: read the release list: %w", err)
	}

	var newest Release
	var newestAsset Asset
	found := false
	for _, r := range page {
		if !tagPattern.MatchString(r.TagName) {
			continue // v1.0.0 and friends: real releases, no binaries on them
		}
		rel := Release{Tag: r.TagName, Published: r.PublishedAt, URL: r.HTMLURL}
		a, ok := pickAsset(r.TagName, runtime.GOOS, runtime.GOARCH, b, r.Assets)
		if !ok {
			continue // a nightly that dropped this platform's build is not an option
		}
		if !found {
			newest, newestAsset, found = rel, a, true
		}
		break
	}
	if !found {
		return Release{}, Asset{}, fmt.Errorf("%w (%s/%s)", ErrNotFound, runtime.GOOS, b)
	}

	if err := m.saveCache(res.Header.Get("ETag"), newest, newestAsset, b); err != nil {
		m.log.Debug("engine: could not cache the release list", "err", err)
	}
	return newest, newestAsset, nil
}

// ---- the asset name -------------------------------------------------------------------

// assetCandidates is why this is a table and not a format string: upstream's names are not
// one pattern. Linux's CPU build is llama-<tag>-bin-ubuntu-x64.tar.gz with no backend word
// at all, Windows' is llama-<tag>-bin-win-cpu-x64.zip with one, and the directory a Linux
// build is labelled with is the distribution family, not the kernel. Trying the candidates
// in order against the release's own asset list means a rename costs an update to the
// table instead of costing a user their update.
func assetCandidates(tag, goos, goarch string, b Backend) []string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	if goarch != "amd64" {
		// Upstream publishes nothing else we could run anyway: the release workflow builds
		// linux/amd64 and windows/amd64 and nothing else.
		return nil
	}

	switch goos {
	case "windows":
		return []string{
			fmt.Sprintf("llama-%s-bin-win-%s-x64.%s", tag, b, ext),
			fmt.Sprintf("llama-%s-bin-win-cpu-x64.%s", tag, ext),
		}
	default: // linux, and anything else that would take the ubuntu build
		names := []string{
			fmt.Sprintf("llama-%s-bin-ubuntu-%s-x64.%s", tag, b, ext),
			fmt.Sprintf("llama-%s-bin-ubuntu-x64.%s", tag, ext),
		}
		if b != Vulkan {
			// A CPU request must not quietly install a Vulkan build.
			names = names[1:]
		}
		return names
	}
}

// upstreamAsset and upstreamRelease are GitHub's JSON, named so that a test can build a
// release page fixture without restating an anonymous struct, and so pickAsset's signature
// says what it takes.
type upstreamAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type upstreamRelease struct {
	TagName     string          `json:"tag_name"`
	PublishedAt time.Time       `json:"published_at"`
	HTMLURL     string          `json:"html_url"`
	Prerelease  bool            `json:"prerelease"`
	Assets      []upstreamAsset `json:"assets"`
}

func pickAsset(tag, goos, goarch string, b Backend, in []upstreamAsset) (Asset, bool) {
	for _, want := range assetCandidates(tag, goos, goarch, b) {
		for _, a := range in {
			if a.Name != want {
				continue
			}
			asset := Asset{Name: a.Name, URL: a.URL, Size: a.Size}
			// GitHub publishes the digest as "sha256:<hex>" and leaves it empty for assets
			// uploaded by hand. Left empty here, Install then refuses.
			if hex, ok := strings.CutPrefix(a.Digest, "sha256:"); ok {
				asset.SHA256 = hex
			}
			return asset, true
		}
	}
	return Asset{}, false
}

// ---- downloading ----------------------------------------------------------------------

// Install fetches, verifies and unpacks a tag, and only then records it. A tag already
// installed is left alone; the point is that an update cannot leave the machine with
// neither the old build nor the new one.
func (m *Manager) Install(ctx context.Context, tag string, b Backend) (Installed, error) {
	if tag == "" || !tagPattern.MatchString(tag) {
		return Installed{}, fmt.Errorf("engine: %q is not a nightly tag", tag)
	}
	if b == "" {
		b = Vulkan
	}

	m.mu.Lock()
	if m.progress.State == StateDownloading || m.progress.State == StateVerifying || m.progress.State == StateUnpacking {
		m.mu.Unlock()
		return Installed{}, ErrBusy
	}
	jobCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.progress = Progress{State: StateDownloading, Tag: tag, Since: m.now().Format(time.RFC3339)}
	m.mu.Unlock()

	in, err := m.install(jobCtx, tag, b)

	m.mu.Lock()
	m.cancel = nil
	if err != nil {
		m.progress.State = StateFailed
		m.progress.Error = err.Error()
	} else {
		m.progress.State = StateDone
		m.progress.Done, m.progress.Total = in.Bytes, in.Bytes
	}
	m.mu.Unlock()
	return in, err
}

// Stop cancels a running download. The .parts file stays, so the next attempt continues
// where the bytes are rather than starting the 35 MB again.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
}

func (m *Manager) Progress() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}

// discardDest removes a half-unpacked engine tree. An archive that fails halfway leaves
// files nothing will ever run and nothing will ever count: the settings page reports the
// engine as not installed, so those bytes are invisible, and the disk has lost a few
// hundred MB to a tree no query can name. This is the same rule the repo already applies to
// a failed write — the temp file goes, whatever else the error was.
func (m *Manager) discardDest(dest string) {
	if err := os.RemoveAll(dest); err != nil {
		m.log.Debug("engine: could not remove the half-unpacked engine", "path", dest, "err", err)
	}
}

func (m *Manager) install(ctx context.Context, tag string, b Backend) (Installed, error) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return Installed{}, fmt.Errorf("engine: %s: %w", m.dir, err)
	}

	// Forced. The caller asked for a tag it read from a page that may itself be older than
	// the cache, and answering "that is not the newest" from a cache nobody refreshed would
	// be a download refused for a bookkeeping artefact. One API call per install is the price.
	rel, asset, err := m.Check(ctx, b, true)
	if err != nil {
		return Installed{}, err
	}
	if rel.Tag != tag {
		// Answering with a tag the caller did not ask for is how a person ends up running a
		// build they never chose, so this is a refusal and not a substitution.
		return Installed{}, fmt.Errorf("engine: asked for %s, the newest release is %s — reload and try again", tag, rel.Tag)
	}
	if asset.SHA256 == "" {
		return Installed{}, fmt.Errorf("engine: %s carries no published digest, so its bytes cannot be verified", asset.Name)
	}
	m.setProgress(func(p *Progress) { p.Asset, p.Total = asset.Name, asset.Size })

	if have, ok := m.Status(); ok {
		if have.Tag == tag && have.Backend == b {
			return have, nil // already what was asked for: not a download, not an error
		}
		// Replacing a build a server is executing is the same mistake the delete refuses, one
		// step earlier: the running process keeps its old code while the directory beneath it
		// becomes a different version, and the next crash is reported against files that no
		// longer match what was loaded. Checked after the short-circuit above, so asking again
		// for what is already installed stays harmless.
		if running(have.Path) {
			return Installed{}, fmt.Errorf("%w — cannot replace it while it runs: %s", ErrRunning, have.Path)
		}
	}
	if free, ok := freeBytes(m.dir); ok && asset.Size > 0 {
		// The archive, the unpacked tree and a little room to breathe. Unpacking a 35 MB
		// archive into 300 MB of tree is the point at which a full disk fails, and a
		// person would rather be told before the download starts.
		if need := asset.Size * 5; free < need {
			return Installed{}, fmt.Errorf("engine: %s needs about %s free, this disk has %s",
				asset.Name, human(need), human(free))
		}
	}

	dest := filepath.Join(m.dir, tag)
	got, resumed, err := m.download(ctx, asset, dest)
	if err != nil {
		return Installed{}, err
	}
	if resumed {
		m.setProgress(func(p *Progress) { p.Resumed = true })
	}
	m.setProgress(func(p *Progress) { p.State = StateVerifying })

	if err := verify(got, asset.SHA256); err != nil {
		// Both halves of the part are deleted, not kept. A corrupt archive that resumes
		// correctly forever is a permanent failure, and re-downloading is the only repair —
		// which is also why the resume record goes with it.
		os.Remove(got)
		os.Remove(m.metaPath(asset))
		return Installed{}, fmt.Errorf("engine: %s: %w (the copy was deleted; the next attempt starts over)", asset.Name, err)
	}

	m.setProgress(func(p *Progress) { p.State = StateUnpacking })
	if err := m.unpack(ctx, got, dest, "llama-"+tag); err != nil {
		m.discardDest(dest)
		return Installed{}, err
	}
	// Both halves of the part go: the archive and its validators. Leaving the .json behind
	// means the next Check finds a resume record for a file that no longer exists, and the
	// .parts directory the settings page offers to discard is never actually empty.
	for _, p := range []string{got, m.metaPath(asset)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Debug("engine: could not delete a leftover part", "path", p, "err", err)
		}
	}

	exe, err := findServer(dest)
	if err != nil {
		m.discardDest(dest)
		return Installed{}, err
	}

	in := Installed{
		Tag:         tag,
		Backend:     b,
		Path:        exe,
		Bytes:       asset.Size,
		InstalledAt: m.now(),
		SHA256:      asset.SHA256,
		Upstream:    rel.Tag,
	}
	raw, err := json.MarshalIndent(in, "", "  ")
	if err == nil {
		err = os.WriteFile(m.indexPath(), append(raw, '\n'), 0o600)
	}
	if err != nil {
		return Installed{}, fmt.Errorf("engine: record the install: %w", err)
	}

	if prev, ok := m.previous(tag); ok {
		// Only now is the older build disposable: until this line nothing had failed.
		if err := os.RemoveAll(prev); err != nil {
			m.log.Warn("engine: updated, but the previous build is still on disk", "path", prev, "err", err)
		}
	}

	m.log.Info("engine installed", "tag", tag, "backend", b, "path", exe, "resumed", resumed)
	return in, nil
}

// previous is the other build directory on disk, which an update replaces.
func (m *Manager) previous(keep string) (string, bool) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() && tagPattern.MatchString(e.Name()) && e.Name() != keep {
			return filepath.Join(m.dir, e.Name()), true
		}
	}
	return "", false
}

func (m *Manager) setProgress(f func(*Progress)) {
	m.mu.Lock()
	f(&m.progress)
	m.mu.Unlock()
}

// ---- the fetch ------------------------------------------------------------------------

// partPath is the file the download lands in, and metaPath records enough to resume it:
// URL, size, and the validators that say the server still has the same bytes.
func (m *Manager) partPath(asset Asset) string {
	return filepath.Join(m.dir, ".parts", asset.Name)
}

func (m *Manager) metaPath(asset Asset) string {
	return m.partPath(asset) + ".json"
}

type partMeta struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	Written      int64     `json:"written"`
	Updated      time.Time `json:"updated"`
}

// download fetches the archive into the .parts directory and returns its path.
//
// Resume is not a nicety: the whole tree is 35 MB and the unpacked engine is larger, and
// the failure mode this is for is a Wi-Fi drop at 90% on a machine the user is about to
// close. A partial file is kept with its validators, and the retry asks the server for the
// rest — with If-Range, so that a server which no longer holds the same file does not
// silently hand back the beginning again and splice two different builds into one binary.
func (m *Manager) download(ctx context.Context, asset Asset, _ string) (path string, resumed bool, err error) {
	if err := os.MkdirAll(filepath.Dir(m.partPath(asset)), 0o755); err != nil {
		return "", false, fmt.Errorf("engine: %s: %w", filepath.Dir(m.partPath(asset)), err)
	}

	part := m.partPath(asset)
	var have partMeta
	if raw, err := os.ReadFile(m.metaPath(asset)); err == nil {
		json.Unmarshal(raw, &have)
	}

	offset := int64(0)
	if fi, statErr := os.Stat(part); statErr == nil && have.URL == asset.URL && have.Size > 0 {
		if fi.Size() <= have.Size {
			offset = fi.Size()
		} else {
			// More bytes than the server says the file has: a truncation of a different
			// shape than a dropped connection, and not resumable.
			os.Remove(part)
			os.Remove(m.metaPath(asset))
		}
	}

	// A part that is already whole is not resumed, it is verified. This is the case a crash
	// between "the bytes are down" and "the bytes were checked" leaves behind, and asking
	// for 35 MB again because the app was closed at the wrong moment is a bad way to spend
	// someone's metered connection.
	if offset > 0 && have.Size > 0 && offset == have.Size && have.SHA256 == asset.SHA256 {
		m.setProgress(func(p *Progress) { p.Done, p.Total, p.Resumed = offset, offset, true })
		return part, true, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", false, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		// If-Range is the part that makes resuming safe rather than lucky: without it a
		// server that has replaced the asset answers 200 with the first byte again, and the
		// two halves of two builds are then concatenated into one file whose hash will never
		// match.
		if have.ETag != "" {
			req.Header.Set("If-Range", have.ETag)
		} else if have.LastModified != "" {
			req.Header.Set("If-Range", have.LastModified)
		}
	}

	res, err := m.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("engine: fetch %s: %w", asset.Name, err)
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusPartialContent:
		resumed = offset > 0
	case http.StatusOK:
		// The server ignored Range, or If-Range said the file changed. Either way the
		// existing bytes are not a prefix of what is coming, so they are discarded.
		if offset > 0 {
			m.log.Debug("engine: the server sent the whole file again", "asset", asset.Name, "had", offset)
		}
		offset = 0
	default:
		return "", false, fmt.Errorf("engine: fetch %s: %s", asset.Name, res.Status)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("engine: %s: %w", part, err)
	}

	total := asset.Size
	if total == 0 {
		total = res.ContentLength + offset
	}
	// The validators are recorded before the body moves, and again wherever it stops. The
	// first version wrote them only on success, so a download that died at 90% left the
	// bytes with no way to be resumed — the one failure mode the .parts directory exists to
	// cover.
	meta := partMeta{
		URL: asset.URL, ETag: res.Header.Get("ETag"), LastModified: res.Header.Get("Last-Modified"),
		Size: total, SHA256: asset.SHA256, Written: offset, Updated: m.now(),
	}
	if res.StatusCode == http.StatusPartialContent && have.ETag != "" {
		meta.ETag = have.ETag
	}
	if offset > 0 {
		meta.ETag, meta.LastModified = have.ETag, have.LastModified
	}
	m.writeMeta(asset, meta)

	w := &countingWriter{f: f, ctx: ctx, done: offset, total: total, m: m}
	stop := func(err error) error {
		meta.Written, meta.Updated = w.done, m.now()
		m.writeMeta(asset, meta)
		f.Close()
		return err
	}

	if _, err := io.Copy(w, res.Body); err != nil {
		return "", false, stop(fmt.Errorf("engine: download %s stopped at %s of %s: %w",
			asset.Name, human(w.done), human(total), err))
	}
	if err := stop(nil); err != nil {
		return "", false, fmt.Errorf("engine: close %s: %w", part, err)
	}
	if total > 0 && w.done != total {
		return "", false, fmt.Errorf("engine: %s ended at %s of %s bytes", asset.Name, human(w.done), human(total))
	}

	meta.Written, meta.Updated = w.done, m.now()
	if res.StatusCode == http.StatusPartialContent && have.ETag != "" {
		// The server confirmed the bytes it is sending continue the ones already on disk, so
		// the validator that made that claim is still the right one to send next time.
		meta.ETag = have.ETag
	}
	m.writeMeta(asset, meta)
	return part, resumed, nil
}

// writeMeta records what a resume needs to know: which file these bytes are the beginning
// of, how big it is supposed to be, and the validator that lets the next attempt ask for
// the rest of *this* copy.
func (m *Manager) writeMeta(asset Asset, meta partMeta) {
	if raw, err := json.Marshal(meta); err == nil {
		if err := os.WriteFile(m.metaPath(asset), raw, 0o600); err != nil {
			m.log.Debug("engine: could not record the partial download", "err", err)
		}
	}
}

// countingWriter is the progress source, and the cancel point: a stopped download must
// notice within a chunk rather than at the end of a 35 MB body.
type countingWriter struct {
	f     *os.File
	ctx   context.Context
	done  int64
	total int64
	m     *Manager
	last  time.Time
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.f.Write(p)
	w.done += int64(n)
	// Polled at most twice a second by the UI, so a write every 64 KB would publish a
	// hundred snapshots a second and show ninety-nine of them nowhere.
	if w.total > 0 && time.Since(w.last) > 500*time.Millisecond {
		w.last = time.Now()
		done := w.done
		w.m.setProgress(func(p *Progress) { p.Done = done })
	}
	return n, err
}

func verify(part, want string) error {
	f, err := os.Open(part)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("engine: hash %s: %w", part, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch: got %s, upstream published %s", got, want)
	}
	return nil
}

// ClearParts removes whatever a stopped or failed download left behind.
func (m *Manager) ClearParts() error {
	if err := os.RemoveAll(filepath.Join(m.dir, ".parts")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("engine: clear the partial download: %w", err)
	}
	return nil
}

// PartsBytes is how much a stopped download is holding on to, which the settings page shows
// next to a "Discard it" button so the number being freed is the number on screen.
func (m *Manager) PartsBytes() int64 {
	var total int64
	filepath.WalkDir(filepath.Join(m.dir, ".parts"), func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// ---- removing -------------------------------------------------------------------------

// Remove deletes the installed engine.
//
// The busy case is returned as an error rather than attempted: on Windows an executable
// cannot be unlinked while it is running, and a half-removed tree is the worst state this
// package can be in — Status() would say "nothing installed" while a llama-server was still
// serving from it.
func (m *Manager) Remove(ctx context.Context) error {
	in, ok := m.Status()
	if !ok {
		return nil // nothing installed is not a failure
	}
	if running(in.Path) {
		return fmt.Errorf("%w — stop the model servers first: %s", ErrRunning, in.Path)
	}
	dir := filepath.Join(m.dir, in.Tag)
	if err := os.RemoveAll(dir); err != nil {
		// A Windows file lock leaves the rest behind. Say which, and do not call it gone.
		if remain, _ := os.ReadDir(dir); len(remain) > 0 {
			return fmt.Errorf("engine: %s could not be removed (something is still using it): %w", dir, err)
		}
		return fmt.Errorf("engine: remove %s: %w", dir, err)
	}
	if err := os.Remove(m.indexPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("engine: remove the record of it: %w", err)
	}
	if err := m.ClearParts(); err != nil {
		m.log.Debug("engine: after removal", "err", err)
	}
	m.log.Info("engine removed", "tag", in.Tag, "path", dir)
	return nil
}

// ---- small helpers --------------------------------------------------------------------

func (m *Manager) indexPath() string { return filepath.Join(m.dir, "installed.json") }
func (m *Manager) cachePath() string { return filepath.Join(m.dir, "index.json") }

func (m *Manager) cachedETag() string {
	raw, err := os.ReadFile(m.cachePath())
	if err != nil {
		return ""
	}
	var c struct {
		ETag string `json:"etag"`
	}
	json.Unmarshal(raw, &c)
	return c.ETag
}

func (m *Manager) cached(b Backend) (Release, Asset, bool) {
	raw, err := os.ReadFile(m.cachePath())
	if err != nil {
		return Release{}, Asset{}, false
	}
	var c struct {
		ETag     string    `json:"etag"`
		Written  time.Time `json:"written"`
		Backend  Backend   `json:"backend"`
		Release  Release   `json:"release"`
		Asset    Asset     `json:"asset"`
		Checksum string    `json:"asset_sha256"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Release.Tag == "" {
		return Release{}, Asset{}, false
	}
	// A cached answer for a different backend is not this one's answer, and age matters:
	// "newest nightly" stops being true as upstream pushes builds.
	if c.Backend != b || m.now().Sub(c.Written) > nightlyAge {
		return Release{}, Asset{}, false
	}
	return c.Release, c.Asset, true
}

func (m *Manager) saveCache(etag string, rel Release, a Asset, b Backend) error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		ETag     string    `json:"etag,omitempty"`
		Written  time.Time `json:"written"`
		Backend  Backend   `json:"backend"`
		Release  Release   `json:"release"`
		Asset    Asset     `json:"asset"`
		Checksum string    `json:"asset_sha256"`
	}{etag, m.now(), b, rel, a, a.SHA256}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.cachePath(), append(raw, '\n'), 0o600)
}

// CompareTags orders two nightly tags. Newest is the bigger number, and an unparsable tag
// loses to a parsable one rather than to chance.
func CompareTags(a, b string) int {
	p := func(s string) int {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "b"))
		if err != nil {
			return -1
		}
		return n
	}
	x, y := p(a), p(b)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
