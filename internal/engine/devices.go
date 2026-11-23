package engine

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"libris/internal/deviceinfo"
)

// probeTimeout bounds a run of the engine's own --list-devices.
//
// Not a guess at how long initialising a GPU takes — that is driver- and box-dependent, and
// a Vulkan init on a laptop that has to wake the dGPU is slow on purpose. It is the limit on
// a process that is supposed to print six lines and exit, so a hang here means a broken
// driver and the answer the user needs is "that one did not answer", not a settings page
// that never comes back.
const probeTimeout = 30 * time.Second

// AskDevices runs an installed engine's --list-devices and returns what it reported.
//
// This is the best device probe available, and the only one that answers the question that
// actually matters later: not what the operating system says is plugged in, which backends
// this build can use and what each of them has free right now. A Vulkan build reports the
// card a CUDA-less driver still has.
//
// It takes a runner rather than calling exec itself for the same reason deviceinfo does: the
// tests must be able to say what a machine reports without owning one, and a unit test that
// needed a GPU would be skipped on the machine it was written to protect.
func AskDevices(ctx context.Context, enginePath string, run deviceinfo.Run) ([]deviceinfo.Device, error) {
	if enginePath == "" {
		return nil, fmt.Errorf("engine: no engine is installed to ask")
	}
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.Output()
		}
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := run(ctx, enginePath, "--list-devices")
	if err != nil {
		return nil, fmt.Errorf("engine: ask %s for its devices: %w", enginePath, err)
	}
	// No rows is a real answer: it is "(none)", meaning this build sees no GPU at all.
	return deviceinfo.ParseDevices(out), nil
}
