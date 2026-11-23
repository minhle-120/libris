package main

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
)

// browseURL turns a bound listener address into a string worth handing to a
// browser. They are not the same thing: a socket may be listening on every
// interface, on no interface in particular, or on a port the kernel picked, and
// none of "http://0.0.0.0:8080", "http://:8080" or ":0" opens a page. The bound
// address is the only version that cannot be wrong, so it is normalised rather
// than the -addr string being pasted.
func browseURL(a net.Addr) string {
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		// Not a host:port pair at all. Nothing better is available; the caller
		// logs the value either way, so a user can still read it.
		return "http://" + a.String()
	}
	switch host {
	case "", "*", "0.0.0.0", "::", "[::]":
		// An unspecified address means "any interface I can reach you on", which
		// a browser cannot be asked to guess. This machine is reachable.
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// browserArgs is the command that asks a desktop to show a URL, keyed by OS name
// rather than by runtime.GOOS so that the Windows path is asserted from a Linux
// box even while only the Linux path is exercised here.
//
// Opening a browser is a courtesy, so a missing helper is a log line and not a
// failure: libris is a server first, and the URL is in the log to paste.
func browserArgs(goos, url string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		// rundll32 rather than `cmd /c start`: the latter needs a shell, and a
		// URL is exactly the kind of string a shell should never be allowed to
		// reinterpret. Here the URL is one argv element all the way down.
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", url}
	default: // linux and the BSDs
		return "xdg-open", []string{url}
	}
}

// showInBrowser is what -open does: a courtesy, never a start-up requirement. No
// desktop session, no xdg-utils, a container — the server is running either way,
// and its address is in the log a line above, so the honest response to a failed
// open is to say that this is what happened rather than to stay silent about it.
func showInBrowser(log *slog.Logger, url string) {
	if err := openBrowser(url); err != nil {
		log.Info("opened no browser; open the url above yourself", "err", err)
	}
}

// openBrowser asks the desktop to show url and returns as soon as the helper has
// started, not when a page is up.
//
// Not waiting is deliberate. xdg-open in particular does not consider its job
// finished until the browser is running, and on a machine with an X server
// installed but not running it may never finish at all. Waiting would turn a
// local-first app into a server that starts late — and, since the helper
// inherits this terminal, one whose Ctrl-C has to answer to a stray child first.
func openBrowser(url string) error {
	name, args := browserArgs(runtime.GOOS, url)
	cmd := exec.Command(name, args...)
	// A child of a terminal must not stay attached to it: nil sends the helper's
	// output to /dev/null rather than into libris's own log, where a browser's
	// complaints would read as libris's.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	// Nobody reads the result. Reaping in a goroutine keeps the child from
	// becoming a zombie libris leaves behind; waiting for it is the thing ruled
	// out above.
	go func() { _ = cmd.Wait() }()
	return nil
}
