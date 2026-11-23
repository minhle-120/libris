package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeUpstream is GitHub for the two endpoints this package uses, in upstream's shapes.
//
// The release page deliberately leads with v1.0.0 carrying one text asset and no binaries,
// because that is what upstream actually publishes: choosing the newest tag by name, or by
// the semver release, is the most likely way to get this wrong and it would fail on a user's
// machine as "no such asset".
type fakeUpstream struct {
	t   *testing.T
	srv *httptest.Server
	// addr is the server's URL; tests set Manager.api to addr + "/api".
	addr string

	tag   string
	etag  string
	asset []byte

	// cut truncates the asset response after N bytes and aborts, which is how a dropped
	// connection looks from here — and is deterministic, unlike cancelling a context
	// halfway through a body.
	cut int
	// tamper replaces the body with something that is not the asset at all.
	tamper []byte

	apiRequests     int
	sawRange        bool
	assetRequests   int
	failIfRequested bool
	sentNotModified bool

	// offerNewer adds a second nightly with binaries, one build ahead of the one the other
	// tests install. Without it there is no way to ask for a *different* tag, and installing
	// the tag already on disk is a no-op that must not be refused.
	offerNewer bool
}

// newerTag is the build offered when offerNewer is set.
func (f *fakeUpstream) newerTag() string { return "b12650" }

func (f *fakeUpstream) newerAssetName() string {
	if runtime.GOOS == "windows" {
		return "llama-" + f.newerTag() + "-bin-win-vulkan-x64.zip"
	}
	return "llama-" + f.newerTag() + "-bin-ubuntu-vulkan-x64.tar.gz"
}

func newFake(t *testing.T) *fakeUpstream {
	t.Helper()

	f := &fakeUpstream{t: t, tag: "b12649", etag: `"nightlies-v1"`}
	f.asset = fakeArchive(t)

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api"):
			f.apiRequests++
			// The validator moves with the content. A fixture that served a different list
			// under the same ETag would make a client that revalidates correctly look broken —
			// which is the kind of bug that gets "fixed" in the client and shipped.
			et := f.etag
			if f.offerNewer {
				et = f.etag + "-with-newer"
			}
			if r.Header.Get("If-None-Match") == et {
				f.sentNotModified = true
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", et)
			w.Header().Set("Content-Type", "application/json")
			extra := ""
			if f.offerNewer {
				extra = fmt.Sprintf(`{"tag_name":%q,"published_at":"2026-11-24T04:00:00Z","html_url":"https://example.invalid/newer",
				   "assets":[{"name":%q,"browser_download_url":"%s/asset","size":%d,"digest":"sha256:%s"}]},`,
					f.newerTag(), f.newerAssetName(), f.addr, len(f.asset), f.digest())
			}
			fmt.Fprintf(w, `[
			  %s{"tag_name":"v1.0.0","published_at":"2026-11-13T10:20:19Z","html_url":"https://example.invalid/v1",
			   "assets":[{"name":"nightly-tag.txt","browser_download_url":"%s/asset","size":12,"digest":"sha256:%s"}]},
			  {"tag_name":"b12649","published_at":"2026-11-23T04:00:00Z","html_url":"https://example.invalid/b12649",
			   "assets":[{"name":"llama-b12649-something-else.zip","browser_download_url":"%s/asset","size":9,"digest":""},
			             {"name":"%s","browser_download_url":"%s/asset","size":%d,"digest":"sha256:%s"}]},
			  {"tag_name":"b12648","published_at":"2026-11-22T04:00:00Z","html_url":"https://example.invalid/b12648","assets":[]}
			]`, extra, f.addr, strings.Repeat("0", 64), f.addr, f.assetName(), f.addr, len(f.asset), f.digest())

		case r.URL.Path == "/asset":
			f.assetRequests++
			if f.failIfRequested {
				f.t.Errorf("the asset was fetched again: %d requests so far", f.assetRequests)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			body := f.asset
			if f.tamper != nil {
				body = f.tamper
			}
			w.Header().Set("ETag", `"asset-v1"`)
			w.Header().Set("Last-Modified", "Sun, 23 Nov 2026 04:00:00 GMT")

			if rng := r.Header.Get("Range"); rng != "" {
				f.sawRange = true
				start := strings.TrimPrefix(rng, "bytes=")
				from, err := strconv.ParseInt(strings.TrimSuffix(start, "-"), 10, 64)
				if err != nil || from >= int64(len(body)) {
					// Answering 200 with the whole file is exactly what a server that has
					// replaced the asset does, and the client must then discard its part.
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					w.WriteHeader(http.StatusOK)
					w.Write(body)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
				w.Header().Set("Content-Length", strconv.Itoa(len(body)-int(from)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(body[from:])
				return
			}

			if f.cut > 0 && f.cut < len(body) {
				// A truncated write followed by an abort: the client sees a short body, and
				// the only thing that can save it is the .part file plus the Range retry.
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(http.StatusOK)
				w.Write(body[:f.cut])
				// Flushed, so the client really holds those bytes: without this the whole
				// response sits in a buffer, the abort discards it, and the test measures a
				// transport artefact instead of a resumable download.
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				panic(http.ErrAbortHandler)
			}
			http.ServeContent(w, r, "asset", time.Time{}, bytes.NewReader(body))

		default:
			http.NotFound(w, r)
		}
	}))
	f.addr = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// serverName is what findServer looks for, spelled for the platform the test is running on.
func serverName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

func (f *fakeUpstream) reset() {
	f.cut, f.tamper, f.sawRange, f.assetRequests, f.failIfRequested = 0, nil, false, 0, false
}

// filler is pseudorandom bytes: a fixture of repeated text gzips down to a few hundred
// bytes, and a "truncated at one third" test on a 587-byte body truncates into a single
// buffered write the transport never delivers. What is being imitated is a 35 MB download,
// and the size has to be of the same order for the truncation to mean the same thing.
func filler(n int) string {
	var b strings.Builder
	b.Grow(n)
	x := uint32(1)
	for b.Len() < n {
		x = x*1103515245 + 12345
		b.WriteByte(byte(x >> 16))
	}
	return b.String()
}

func (f *fakeUpstream) assetName() string {
	if runtime.GOOS == "windows" {
		return "llama-" + f.tag + "-bin-win-vulkan-x64.zip"
	}
	return "llama-" + f.tag + "-bin-ubuntu-vulkan-x64.tar.gz"
}

// digest is the digest upstream publishes, which is of the real asset — always. The earlier
// version hashed f.tamper when set, which had the fixture publishing a digest that matched
// its own tampering and so tested nothing.
func (f *fakeUpstream) digest() string {
	h := sha256.Sum256(f.asset)
	return hex.EncodeToString(h[:])
}

// fakeArchive builds the real archive shape for this platform: one shared root directory,
// bin/ with the server, lib/ beside it. Zip on Windows because that is what upstream ships
// there, tar.gz on everything else.
func fakeArchive(t *testing.T) []byte {
	t.Helper()
	const exe = "llama-server"

	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		zw.Create("llama-b12649/bin/")
		fw, err := zw.Create("llama-b12649/bin/" + exe + ".exe")
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(fw, "MZ fake executable, long enough to be a real size\n")
		fw2, _ := zw.Create("llama-b12649/lib/ggml-vulkan.dll")
		io.WriteString(fw2, filler(200_000))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, mode int64, body string) {
		tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
			Format: tar.FormatGNU,
		})
		io.WriteString(tw, body)
	}
	write("llama-b12649/bin/"+exe, 0o755, "#!/bin/sh\nexit 0\n")
	write("llama-b12649/lib/libggml-cpu.so", 0o644, filler(200_000))
	write("llama-b12649/lib/libggml-base.so", 0o644, filler(120_000))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// ---- the flow -------------------------------------------------------------------------

func TestInstallFetchesVerifiesUnpacksAndRecords(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	m := Open(dir, nil)
	m.api = f.addr + "/api"

	rel, asset, err := m.Check(context.Background(), Vulkan, false)
	if err != nil {
		t.Fatal(err)
	}
	// The newest entry upstream published is v1.0.0 and it has no binaries on it. Picking a
	// tag by name, or by "latest release", is the mistake this test exists to prevent.
	if rel.Tag != "b12649" {
		t.Errorf("newest usable nightly = %q, want b12649", rel.Tag)
	}
	if asset.Name != f.assetName() {
		t.Errorf("asset = %q, want %s", asset.Name, f.assetName())
	}
	if asset.SHA256 != f.digest() {
		t.Errorf("digest = %q, want the published one with its prefix removed", asset.SHA256)
	}

	in, err := m.Install(context.Background(), rel.Tag, Vulkan)
	if err != nil {
		t.Fatal(err)
	}
	if in.Path == "" {
		t.Fatal("installed, with no path to the server binary")
	}
	want := filepath.Join(dir, "b12649")
	if !strings.HasPrefix(filepath.Clean(in.Path), filepath.Clean(want)) {
		t.Errorf("path = %s, want it under %s", in.Path, want)
	}
	if _, err := os.Stat(in.Path); err != nil {
		t.Errorf("the recorded binary is not there: %v", err)
	}
	// The archive's own root directory is stripped, so the tree is bin/ under the tag and
	// not bin/ under a second copy of the tag.
	if _, err := os.Stat(filepath.Join(want, "bin", serverName())); err != nil {
		t.Errorf("the tree was not laid out where libris looks: %v", err)
	}
	if _, err := findServer(want); err != nil {
		t.Errorf("findServer: %v", err)
	}
	if b := m.PartsBytes(); b != 0 {
		t.Errorf("%d bytes left in .parts after a completed install", b)
	}
	if p := m.Progress(); p.State != StateDone || p.Done != p.Total {
		t.Errorf("progress = %+v, want done with the byte counts equal", p)
	}

	have, ok := m.Status()
	if !ok || have.Tag != "b12649" || have.Backend != Vulkan || have.InstalledAt.IsZero() {
		t.Fatalf("Status after install: %+v ok=%v", have, ok)
	}

	// Opening the settings page again must not spend the hourly unauthenticated allowance.
	before := f.apiRequests
	if _, _, err := m.Check(context.Background(), Vulkan, false); err != nil {
		t.Fatal(err)
	}
	if f.apiRequests != before {
		t.Errorf("the second visit made %d API calls, want the cached answer", f.apiRequests-before)
	}
	// "Check now" is the exception, and it revalidates rather than refetching.
	if _, _, err := m.Check(context.Background(), Vulkan, true); err != nil {
		t.Fatal(err)
	}
	if !f.sentNotModified {
		t.Error("a forced check did not send If-None-Match, so it refetches a page that has not changed")
	}
}

func TestCheckOfflineFallsBackToTheCachedAnswer(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"
	if _, _, err := m.Check(context.Background(), Vulkan, false); err != nil {
		t.Fatal(err)
	}

	// A closed port is what "no network" looks like from here.
	m.api = "http://127.0.0.1:1/api"
	rel, _, err := m.Check(context.Background(), Vulkan, false)
	if err != nil {
		t.Fatalf("offline with a usable cache: %v", err)
	}
	if rel.Tag != "b12649" {
		t.Errorf("cached tag = %q", rel.Tag)
	}

	// A different backend is not this backend's answer: a Vulkan cache must not be used to
	// offer a CPU build or the download would be the wrong one silently.
	if _, _, err := m.Check(context.Background(), CPU, false); err == nil {
		t.Error("the Vulkan cache answered a CPU query")
	}
}

func TestInstallRefusesBytesThatDoNotMatch(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"
	// A captive portal, a truncated mirror, a partial file from a different build: every one
	// of them arrives here, and an unpinned download is only safe because they do.
	// Same length as the real asset with one byte flipped: a short body is caught by the
	// byte count, and this is the case where only the digest can tell.
	f.tamper = append([]byte(nil), f.asset...)
	f.tamper[len(f.tamper)/2] ^= 0xff

	_, err := m.Install(context.Background(), "b12649", Vulkan)
	if err == nil {
		t.Fatal("a binary whose digest does not match upstream's was accepted")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("err = %v, want it to name the check that failed", err)
	}
	if _, ok := m.Status(); ok {
		t.Error("a failed install is recorded as installed")
	}
	if b := m.PartsBytes(); b != 0 {
		t.Errorf("%d bytes left in .parts: keeping them means every later attempt resumes the corruption", b)
	}
	if p := m.Progress(); p.State != StateFailed || p.Error == "" {
		t.Errorf("progress = %+v, want a failed state the settings page can show verbatim", p)
	}
}

func TestStoppedDownloadResumesRatherThanRestarting(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"

	f.cut = len(f.asset) / 3
	if _, err := m.Install(context.Background(), "b12649", Vulkan); err == nil {
		t.Fatal("a truncated download reported success")
	}
	have := m.PartsBytes()
	if have < int64(f.cut) {
		t.Fatalf("%d bytes kept of the %d written, so the next attempt starts over", have, f.cut)
	}

	f.reset()
	in, err := m.Install(context.Background(), "b12649", Vulkan)
	if err != nil {
		t.Fatalf("the retry did not finish: %v", err)
	}
	if !f.sawRange {
		t.Error("the retry asked for the whole file again — no Range header was sent")
	}
	if !m.Progress().Resumed {
		t.Error("progress does not say the install resumed")
	}
	if in.Path == "" {
		t.Error("the resumed install recorded no path")
	}
	if b := m.PartsBytes(); b != 0 {
		t.Errorf("%d bytes left in .parts after finishing", b)
	}
}

// A part that is already whole is verified where it lies. This is the state a crash between
// the last byte arriving and the hash running leaves behind, and re-fetching 35 MB because
// the app was closed at the wrong moment is a bad way to spend someone's connection.
func TestCompletePartIsVerifiedNotRefetched(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	m := Open(dir, nil)
	m.api = f.addr + "/api"

	_, asset, err := m.Check(context.Background(), Vulkan, false)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "b12649")
	part, _, err := m.download(context.Background(), asset, dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(part); err != nil || fi.Size() != int64(len(f.asset)) {
		t.Fatalf("the first download did not complete: %v", err)
	}
	calls := f.assetRequests

	if _, resumed, err := m.download(context.Background(), asset, dest); err != nil {
		t.Fatalf("a complete part was rejected: %v", err)
	} else if !resumed {
		t.Error("a part that was already whole was not reported as resumed")
	}
	if f.assetRequests != calls {
		t.Errorf("the asset was fetched %d times, want it left at %d", f.assetRequests, calls)
	}
}

func TestRemoveRefusesWhileTheEngineIsRunning(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"
	in, err := m.Install(context.Background(), "b12649", Vulkan)
	if err != nil {
		t.Fatal(err)
	}

	// The process table is reached through a variable so this can be the case where
	// something is running: a unit test may not launch llama-server, and one that skipped on
	// that ground would defend nothing. Deleting a running engine's files is the one action
	// here that must be refused rather than attempted — on Windows the unlink fails partway
	// and leaves a tree that reads as uninstalled while a server keeps serving from it.
	was := running
	running = func(exe string) bool {
		if exe != in.Path {
			t.Errorf("asked whether %q runs, want the installed binary %q", exe, in.Path)
		}
		return true
	}

	err = m.Remove(context.Background())
	running = was
	if err == nil {
		t.Fatal("deleted the binaries a running server is using")
	}
	if !strings.Contains(err.Error(), "stop") {
		t.Errorf("err = %v, want it to name the reason and the fix", err)
	}
	if _, ok := m.Status(); !ok {
		t.Error("a refused delete changed the install")
	}

	running = func(string) bool { return false }
	if err := m.Remove(context.Background()); err != nil {
		t.Fatalf("delete with nothing running: %v", err)
	}
	if _, ok := m.Status(); ok {
		t.Error("still installed after a successful delete")
	}
	if entries, _ := os.ReadDir(filepath.Join(m.Dir(), "b12649")); len(entries) != 0 {
		t.Errorf("the build directory survived the delete: %v", entries)
	}
	// Deleting twice is not an error: the page may show a stale row.
	if err := m.Remove(context.Background()); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
}

func TestInstallOfAnOlderTagDoesNotPretend(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"

	// b12648 is on the page and has no asset for this platform, so it is not offered — and
	// asking for it by hand must be refused rather than quietly installing b12649.
	if _, err := m.Install(context.Background(), "b12648", Vulkan); err == nil {
		t.Error("installing a tag with no build for this platform reported success")
	}
	if _, err := m.Install(context.Background(), "v1.0.0", Vulkan); err == nil {
		t.Error("a semver tag was accepted as an engine version")
	}
}

// ---- the parts, on their own ----------------------------------------------------------

func TestPickAssetUsesUpstreamsOwnNames(t *testing.T) {
	in := func(names ...string) []upstreamAsset {
		var out []upstreamAsset
		for _, n := range names {
			out = append(out, upstreamAsset{Name: n, URL: "https://example.invalid/" + n,
				Size: 35 << 20, Digest: "sha256:" + strings.Repeat("ab", 32)})
		}
		return out
	}

	cases := []struct {
		goos    string
		backend Backend
		want    string
	}{
		{"linux", Vulkan, "llama-b12649-bin-ubuntu-vulkan-x64.tar.gz"},
		{"windows", Vulkan, "llama-b12649-bin-win-vulkan-x64.zip"},
		{"linux", CPU, "llama-b12649-bin-ubuntu-x64.tar.gz"},
		{"windows", CPU, "llama-b12649-bin-win-cpu-x64.zip"},
	}
	for _, c := range cases {
		got, ok := pickAsset("b12649", c.goos, "amd64", c.backend, in(c.want, "llama-b12649-bin-win-opencl-adreno-arm64.zip"))
		if !ok || got.Name != c.want {
			t.Errorf("%s/%s: got %+v want %s", c.goos, c.backend, got, c.want)
		}
		if got.SHA256 != strings.Repeat("ab", 32) {
			t.Errorf("%s/%s: digest = %q, want the hex with its algorithm prefix cut off", c.goos, c.backend, got.SHA256)
		}
	}

	// A CPU request must not be answered with a Vulkan build: the machine asked for the safe
	// one and would get a different failure than it was braced for.
	if got, ok := pickAsset("b12649", "linux", "amd64", CPU, in("llama-b12649-bin-ubuntu-vulkan-x64.tar.gz")); ok {
		t.Errorf("a cpu request picked %s", got.Name)
	}
	// Upstream publishes nothing else that could run: the release workflow cross-compiles
	// linux/amd64 and windows/amd64 and nothing else.
	if _, ok := pickAsset("b12649", "linux", "arm64", Vulkan, in("anything")); ok {
		t.Error("arm64 was offered a build that does not exist")
	}
	// No digest on the asset means nothing to verify against, and Install refuses — that
	// refusal is tested by TestInstallRefusesBytesThatDoNotMatch, and starts here.
	noDigest := []upstreamAsset{{Name: "llama-b12649-bin-ubuntu-vulkan-x64.tar.gz", URL: "u"}}
	if a, ok := pickAsset("b12649", "linux", "amd64", Vulkan, noDigest); !ok || a.SHA256 != "" {
		t.Errorf("a digestless asset came back as %+v, want it selected with an empty SHA256 so Install can refuse it", a)
	}
}

func TestCompareTagsOrdersNightlies(t *testing.T) {
	if CompareTags("b12649", "b12648") != 1 {
		t.Error("b12649 must be newer than b12648")
	}
	// Lexically "b9999" sorts after "b12649" because '9' > '1'. It is the older build, and a
	// version check that compared strings would report an update back to it.
	if CompareTags("b9999", "b12649") != -1 {
		t.Error("nightlies are compared as strings somewhere: b9999 is older than b12649")
	}
	if CompareTags("b12649", "b12649") != 0 {
		t.Error("equal tags must compare equal")
	}
	if CompareTags("", "b12649") >= 0 || CompareTags("v1.0.0", "b12649") >= 0 {
		t.Error("an unparsable tag must lose to a nightly rather than win by chance")
	}
}

func TestPsArgsMatchesThePathNotTheName(t *testing.T) {
	out := []byte("/opt/llama/bin/llama-server --model x.gguf\n" +
		"/home/u/Documents/libris/bin/b12649/llama-server --port 8081\n")
	if !psArgs(out, "/home/u/Documents/libris/bin/b12649/llama-server") {
		t.Error("libris's own server was not found")
	}
	// The same program from elsewhere is somebody else's process. Blocking a delete on it
	// would be a false claim about what is using these files.
	if psArgs(out, "/home/u/Documents/libris/bin/b12000/llama-server") {
		t.Error("a different build's path blocked its own deletion")
	}
}

func TestArchiveMembersCannotEscape(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../../evil", "/etc/evil", "a/../../evil", "C:/evil", "..", "a/../.."} {
		if p, err := safeJoin(dir, name); err == nil {
			t.Errorf("%q was accepted, producing %s", name, p)
		}
	}
	if p, err := safeJoin(dir, "bin/llama-server"); err != nil || !strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)) {
		t.Errorf("an ordinary member was refused: %s %v", p, err)
	}
	if got := stripRoot("llama-b12649/bin/llama-server", "llama-b12649"); got != "bin/llama-server" {
		t.Errorf("stripRoot = %q, want the path under upstream's build directory", got)
	}
	if got := stripRoot("llama-b12649/", "llama-b12649"); got != "" {
		t.Errorf("the build root entry became %q, want nothing written", got)
	}
	// The case that made this rule exact rather than inferential: applied to a member two
	// levels up, the old "everything before the first slash" rule returned "escaped" — a path
	// safeJoin would accept. Nothing is stripped here that was not upstream's own directory.
	// What matters is the composition: whatever stripRoot leaves, safeJoin has to refuse.
	// stripRoot's job is only to not *create* a safe path out of an unsafe one.
	for _, name := range []string{"../escaped", "llama-b12649/../../escaped"} {
		got := stripRoot(name, "llama-b12649")
		if _, err := safeJoin(dir, got); err == nil {
			t.Errorf("stripRoot(%q) = %q, and that path was then accepted", name, got)
		}
	}
	if got := stripRoot("other-root/bin/x", "llama-b12649"); got != "other-root/bin/x" {
		t.Errorf("a member under a different root became %q", got)
	}
}

func TestUnpackKeepsTheLinuxModes(t *testing.T) {
	// The tar carries modes, so unlike the zip path this one must preserve them rather than
	// invent them: an engine that extracts non-executable does not start.
	dir := t.TempDir()
	m := Open(dir, nil)
	archive := filepath.Join(dir, "engine.tar.gz")
	if err := os.WriteFile(archive, fakeArchive(t), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "b12649")
	if err := m.unpack(context.Background(), archive, dest, "llama-b12649"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "bin", "llama-server"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode = %v, want the archive's executable bit kept", fi.Mode().Perm())
	}
	if _, err := findServer(dest); err != nil {
		t.Errorf("findServer: %v", err)
	}
}

func TestUnpackRefusesAnEscapingMember(t *testing.T) {
	// The archive is as untrusted as a filename from a browser, and this repo already refuses
	// rather than sanitises (DirSink). Writing outside <data>/bin is not a thing that happens.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// "../escaped" is the classic; "llama-b12649/../../escaped" is the one that matters more,
	// because it *looks* like a member under the build root and the first version of
	// stripRoot turned it into "escaped" — a path safeJoin would happily accept.
	fw, _ := zw.Create("../escaped")
	io.WriteString(fw, "no")
	fw2, _ := zw.Create("llama-b12649/../../escaped2")
	io.WriteString(fw2, "no")
	zw.Close()

	dir := t.TempDir()
	m := Open(dir, nil)
	archive := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.unpack(context.Background(), archive, filepath.Join(dir, "b12649"), "llama-b12649"); err == nil {
		t.Fatal("an escaping archive member was written")
	}
	for _, name := range []string{"escaped", "escaped2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was written next to the destination", name)
		}
	}
}

func TestStatusIgnoresATreeWithNoRecord(t *testing.T) {
	// A directory named for a nightly with no installed.json is a download that died, and
	// calling it installed would let libris try to exec half a tree.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "b12649", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Open(dir, nil)
	if in, ok := m.Status(); ok {
		t.Errorf("a directory with no record read as installed: %+v", in)
	}
}

func TestProxyIsAppliedToTheDownloadOnly(t *testing.T) {
	// A mirror that serves release files almost never mirrors the API, so silently pointing
	// the version query at a stranger's box is a worse failure than a download that needs a
	// second try.
	m := Open(t.TempDir(), nil)
	m.SetProxy("https://mirror.example/")
	if got := m.downloadURL("https://github.com/x/y.zip"); got != "https://mirror.example/https://github.com/x/y.zip" {
		t.Errorf("download URL = %q", got)
	}
	m.SetProxy("")
	if got := m.downloadURL("https://github.com/x/y.zip"); got != "https://github.com/x/y.zip" {
		t.Errorf("with no proxy: %q", got)
	}
}

// TestUpdateCheckIsBoundedEvenWhenTheServerIsSilent covers the difference between the two
// HTTP clients this package owns. A release list is a few kilobytes the settings page waits
// on, so it gets a deadline; the archive does not, because a 35 MB fetch over a slow link is
// supposed to be long. A single client for both would either kill the download or let a hung
// api.github.com hold the page open indefinitely — and "indefinitely" is the interesting word,
// because a socket that accepts and says nothing never errors on its own.
func TestUpdateCheckIsBoundedEvenWhenTheServerIsSilent(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang // accepted, answered never
	}))

	m := Open(t.TempDir(), nil)
	m.api = srv.URL + "/api"
	m.release = &http.Client{Timeout: 60 * time.Millisecond}

	start := time.Now()
	_, _, err := m.Check(context.Background(), Vulkan, true)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a server that never answered produced no error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the check waited %s, want it cut off by its own deadline", elapsed)
	}
}
