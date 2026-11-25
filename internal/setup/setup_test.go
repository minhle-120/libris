package setup

import (
	"slices"
	"testing"
)

func completeMachine() Input {
	return Input{
		Managed: true, EngineInstalled: true,
		Ack: "b12674:machine-1|engine=b12674|linux/amd64", AckNow: "b12674:machine-1|engine=b12674|linux/amd64",
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

// The confirmation is about this engine build on this machine: a new build reports a
// different device list, and a directory copied onto other hardware has never been shown the
// row. A reboot is neither of those. It changes what the machine can be asked — that is the
// device cache's key, which does move — but not who was already told, so a key that moved
// with it reopened the setup screen on every start of a machine that was set up.
func TestConfirmationIsKeyedToEngineAndMachine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ackNow string
		device bool
	}{
		{"the same machine, next start-up", "b12674:machine-1|engine=b12674|linux/amd64", true},
		{"a newer engine build", "b12700:machine-1|engine=b12700|linux/amd64", false},
		{"a copied data directory", "b12674:machine-2|engine=b12674|linux/amd64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := completeMachine()
			in.AckNow = tc.ackNow
			v := For(in)

			if v.Device != tc.device {
				t.Errorf("device = %v, want %v for %q", v.Device, tc.device, tc.ackNow)
			}
			if !tc.device && v.Step != string(FactDevice) {
				t.Errorf("step = %q, want device", v.Step)
			}
		})
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

// The gate is the wizard being the screen. A machine that has done everything a person
// can do, while libris's own start-up brings its model server up, has nothing left to ask
// — and every cold start used to answer it with a wizard anyway, because the gate was read
// once at boot and the router takes longer to load a model than the page takes to ask.
func TestTheWizardDoesNotWaitForAProcessLibrisIsStarting(t *testing.T) {
	going := completeMachine()
	going.Autostart = true
	v := For(going)
	if v.Ready {
		t.Fatal("a router that is not up would not answer a question")
	}
	if !v.Starting {
		t.Errorf("the only thing missing is a process libris is starting: %+v", v)
	}
	if v.Step != "" {
		t.Errorf("step = %q, want none: there is nothing left to ask", v.Step)
	}

	// Nobody is starting it — the machine only finished its steps part-way through a
	// session, after start-up had already stood down. Here the wizard is the only thing
	// with a Start button, so it is the screen, and a flag invented from the missing fact
	// alone would have walked past it into an app that answers nothing.
	if For(completeMachine()).Starting {
		t.Error("a missing router with no start in flight opened the gate anyway")
	}

	up := completeMachine()
	up.Serving = true
	up.Autostart = true
	if w := For(up); w.Starting || !w.Ready {
		t.Errorf("a running router is not a start in flight: %+v", w)
	}

	// A machine still waiting on a person stays behind the gate, even though the router is
	// missing from it too — Starting means nothing left to ask, not merely something running.
	noDevice := completeMachine()
	noDevice.Ack = ""
	noDevice.Autostart = true
	if For(noDevice).Starting {
		t.Error("an unconfirmed device is a step for a person, not a process libris is starting")
	}

	// Unmanaged mode runs nothing, so there is no start for a wizard to wait on.
	if For(Input{Managed: false}).Starting {
		t.Error("external mode reported a start it does not own")
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
