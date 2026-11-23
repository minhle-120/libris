package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBrowseURLNamesAnAddressABrowserCanOpen(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}, "http://127.0.0.1:8080"},
		// Listening on everything is not an address a browser can be handed.
		{&net.TCPAddr{IP: net.IPv4zero, Port: 8080}, "http://127.0.0.1:8080"},
		{&net.TCPAddr{IP: net.IPv6zero, Port: 8080}, "http://127.0.0.1:8080"},
		{&net.TCPAddr{Port: 8080}, "http://127.0.0.1:8080"},
		// A specific non-loopback address stays itself: someone who bound one
		// means that interface, and localhost would not prove it works.
		{&net.TCPAddr{IP: net.ParseIP("192.168.1.10"), Port: 8080}, "http://192.168.1.10:8080"},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 8080}, "http://[::1]:8080"},
		// Anything that is not a host:port pair still has to produce a string.
		{&net.UnixAddr{Name: "/tmp/libris.sock", Net: "unix"}, "http:///tmp/libris.sock"},
	} {
		if got := browseURL(tc.addr); got != tc.want {
			t.Errorf("browseURL(%v) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestBrowseURLKeepsAPortTheKernelChose(t *testing.T) {
	// -addr 127.0.0.1:0 is how you run next to another libris. The bound port,
	// not the requested zero, is the only thing worth opening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := browseURL(ln.Addr())
	if !strings.HasPrefix(got, "http://127.0.0.1:") || strings.HasSuffix(got, ":0") {
		t.Errorf("browseURL(%v) = %q, want a real port", ln.Addr(), got)
	}
}

func TestBrowserArgsHandsTheURLAsOneArgument(t *testing.T) {
	// The URL is a query string in the shape of a shell command line. Whatever the
	// helper, it arrives as one argv element and never as text for a shell to
	// reinterpret.
	const url = "http://127.0.0.1:8080/?a=b&c=d"
	for _, goos := range []string{"linux", "darwin", "windows"} {
		name, args := browserArgs(goos, url)
		if name == "" {
			t.Errorf("%s: no helper named", goos)
			continue
		}
		if len(args) == 0 || args[len(args)-1] != url {
			t.Errorf("%s: %s %q does not end with the url", goos, name, args)
		}
		for _, a := range args {
			if strings.ContainsAny(a, ";|`") {
				t.Errorf("%s: %q looks like it was meant for a shell", goos, a)
			}
		}
	}
}

func TestOpenBrowserReportsAMissingHelperInsteadOfFailing(t *testing.T) {
	// No helper on PATH is the headless case. It must come back as an error the
	// caller logs, not a panic and not a hang.
	t.Setenv("PATH", "")
	name, _ := browserArgs(runtime.GOOS, "http://127.0.0.1:8080/")

	err := openBrowser("http://127.0.0.1:8080/")
	if err == nil {
		t.Fatal("openBrowser succeeded with no helper on PATH")
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("err = %v, want it to name the helper %q", err, name)
	}
}

func TestOpenBrowserDoesNotWaitForTheHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script")
	}
	// xdg-open stays alive until a browser is up, and can stay alive forever with
	// an X server installed but not running. libris is a server: it starts anyway.
	dir := t.TempDir()
	name, _ := browserArgs(runtime.GOOS, "http://127.0.0.1:1/")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	start := time.Now()
	if err := openBrowser("http://127.0.0.1:1/"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("openBrowser blocked for %v waiting for the helper", d)
	}
}
