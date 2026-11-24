package engine

import (
	"fmt"
	"net"
	"testing"
)

// freePort is the whole port policy on Auto, so it is worth asserting the one thing the
// caller cannot see: that the number it returns is a port nothing was holding when it was
// asked. The race after it is closed is deliberate and documented, not a bug this test can
// remove — binding the number again immediately is the check that the promise made is the
// promise kept.
func TestFreePortHandsBackAPortNobodyHolds(t *testing.T) {
	for range 3 {
		port, err := freePort("127.0.0.1")
		if err != nil {
			t.Fatalf("freePort: %v", err)
		}
		if port <= 0 || port > 65535 {
			t.Fatalf("freePort returned %d, which is not a port", port)
		}

		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("freePort returned %d and then something else had it: %v", port, err)
		}
		l.Close()
	}
}

// The address asked for is the address bound, because that is the one llama.cpp will be
// handed too. What matters here is the failure: an address that cannot be listened on is an
// error, not an invented port — a number made up here would be reported as "the engine is
// running on it" and then nothing would answer.
func TestFreePortFailsRatherThanInventAPort(t *testing.T) {
	if _, err := freePort("240.0.0.1"); err == nil {
		t.Fatal("freePort returned a port for an address that cannot be listened on")
	}
}
