package engine

import (
	"fmt"
	"net"
)

// autoPortAttempts bounds how many times a start on Auto asks for a port. A collision on a
// port the kernel just handed back means something else on this machine is racing for the
// same socket numbers; four tries is enough to tell that apart from a bad millisecond
// without turning a start into a loop nobody can interrupt.
const autoPortAttempts = 4

// noPortSentence is what a start on Auto says when *every* attempt collided — a different
// sentence from portSentence, because "change the port in Settings" would be advice to go
// and pin the very thing that was avoided.
const noPortSentence = "No free port could be found on this machine. " +
	"Something here is claiming ports as fast as they are released — set a port in Settings to say which one to use."

// freePort asks the kernel for a port nothing is holding: bind :0, read the number back, let
// go again.
//
// This is how libris learns a free port, rather than handing llama.cpp --port 0 and letting
// it bind any port it likes. That works — llama.cpp calls bind_to_any_port and adopts the
// number — and then the only place the chosen address exists is that build's startup log.
// libris downloads unpinned builds, so scraping a log line for the address every
// request is aimed at would be a wire claim about a format that is allowed to move between
// releases. Asking here costs one socket and keeps the knowledge on this side.
//
// What this does not do is make a collision impossible. Between closing this listener and
// llama-server binding the same number there is a window, and anything may take the port in
// it. Start retries on Auto for exactly that reason; a pinned port is not retried, because
// the reason to name a port is usually that something else was already told that address.
func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("engine: ask for a free port: %w", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port
	if port <= 0 {
		return 0, fmt.Errorf("engine: ask for a free port: got %d", port)
	}
	return port, nil
}
