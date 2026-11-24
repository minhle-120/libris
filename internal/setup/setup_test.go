package setup

import (
	"slices"
	"testing"
)

func completeMachine() Input {
	return Input{
		Managed: true, EngineInstalled: true,
		Ack: "b12674:boot-1|engine=b12674|linux/amd64", AckNow: "b12674:boot-1|engine=b12674|linux/amd64",
		ChatComplete: true, EmbedComplete: true,
	}
}

func TestFreshInstallNamesTheEngineFirst(t *testing.T) {
	v := For(Input{Managed: true})
	if v.Ready {
		t.Fatal("nothing installed is not ready")
	}
	if v.Step != string(FactEngine) {
		t.Errorf("step = %q, want engine", v.Step)
	}
	// Everything missing is listed, in the order a person would fix it. The screen
	// shows the later steps as waiting rather than hiding them.
	want := []Fact{FactEngine, FactModels, FactServing}
	if !slices.Equal(v.Blocking, want) {
		t.Errorf("blocking = %v, want %v", v.Blocking, want)
	}
}

// An empty device setting means two different things — libris picked, or nobody was
// asked — and only the confirmation tells them apart. Without it the step passes the
// moment an engine lands on disk, which is the bug the confirmation exists to stop.
func TestUnconfirmedDeviceIsAStepNotAPass(t *testing.T) {
	in := completeMachine()
	in.Ack = ""
	v := For(in)
	if v.Device || v.Ready {
		t.Errorf("an unasked device step passed: %+v", v)
	}
	if v.Step != string(FactDevice) {
		t.Errorf("step = %q, want device", v.Step)
	}
	if !v.Engine || !v.Models {
		t.Errorf("the steps that are done were not shown as done: %+v", v)
	}
}

// The confirmation is about this engine build on this machine. A reboot, a new build
// with a different device list, or a data directory copied onto other hardware all
// mean the row has never been shown for what is here now.
func TestConfirmationIsKeyedToEngineAndMachine(t *testing.T) {
	in := completeMachine()
	in.AckNow = "b12674:boot-2|engine=b12674|linux/amd64" // rebooted
	v := For(in)
	if v.Device {
		t.Errorf("a stale confirmation answered for a machine never shown the row: %+v", v)
	}
	if v.Step != string(FactDevice) {
		t.Errorf("step = %q, want device", v.Step)
	}
}

// A turn needs both roles. A router holding only an embedder indexes uploads and
// fails every question — which is what start-up used to bring up, on "any one
// complete".
func TestOneModelIsNotASetup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		chat, embed bool
	}{
		{"embed only", false, true},
		{"chat only", true, false},
		{"neither", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := completeMachine()
			in.ChatComplete, in.EmbedComplete = tc.chat, tc.embed
			in.Serving = true // even a running router does not make the missing role appear
			v := For(in)
			if v.Models || v.Ready || !slices.Contains(v.Blocking, FactModels) {
				t.Errorf("one role counted as a setup: %+v", v)
			}
			if v.Step != string(FactModels) {
				t.Errorf("step = %q, want models", v.Step)
			}
			if Startable(in) {
				t.Error("start-up would run a router that cannot answer")
			}
		})
	}
}

func TestReadyWhenEveryFactHolds(t *testing.T) {
	in := completeMachine()
	in.Serving = true
	v := For(in)
	if !v.Ready || v.Step != "" || len(v.Blocking) != 0 {
		t.Errorf("a finished machine: %+v", v)
	}
	if !slices.Equal(v.Blocking, []Fact(nil)) {
		t.Errorf("blocking = %v, want none", v.Blocking)
	}
}

// Start-up's question is narrower than readiness: may libris bring the router up, or
// is a person still needed? Exactly one thing missing, and that thing is the process,
// is the only yes.
func TestStartableIsEverythingButTheProcess(t *testing.T) {
	in := completeMachine()
	if !Startable(in) {
		t.Error("a settled machine should be started")
	}

	serving := completeMachine()
	serving.Serving = true
	if Startable(serving) {
		t.Error("a router that is already up is not a thing to start")
	}

	noDevice := completeMachine()
	noDevice.Ack = ""
	if Startable(noDevice) {
		t.Error("start-up started a router before anyone confirmed the device")
	}

	noEngine := completeMachine()
	noEngine.EngineInstalled = false
	if Startable(noEngine) {
		t.Error("start-up started an engine that is not on disk")
	}
}

// External mode has no engine, no device list and no process here: the URLs are the
// whole configuration, and libris cannot tell whether somebody else's servers answer.
func TestExternalModeHasNothingToSetUp(t *testing.T) {
	v := For(Input{Managed: false})
	if !v.Ready || v.Step != "" || len(v.Blocking) != 0 {
		t.Errorf("external mode offered a wizard: %+v", v)
	}
	if !v.Engine || !v.Device || !v.Models || !v.Serving {
		t.Errorf("external mode claimed steps about things it does not run: %+v", v)
	}
	if Startable(Input{Managed: false}) {
		t.Error("external mode would start a server it does not own")
	}
}
