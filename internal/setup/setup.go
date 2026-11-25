// Package setup answers one question: can libris answer a question yet, and if not,
// what is the next thing to do about it?
//
// The question is answered in two places by necessity — the settings screen renders
// it, and libris's own start-up decides whether to bring the model server up — and
// it used to be answered twice, in two languages, from two different rules. The
// screen required both models and a confirmed device choice; start-up required any
// one model and could not see the device confirmation at all, because that answer
// lived in a browser tab. So libris started a router that could not answer anything,
// on a machine whose device choice nobody had looked at, and then turned the
// screen's last step green for it.
//
// One rule, computed once, with the inputs handed to it:
//
//	ready = the engine is here, and the device choice has been confirmed for this
//	        engine on this machine, and both roles have a model, and the router is up
//
// The browser is a courier for the confirmation and a renderer for the result. It
// holds no rule of its own, so it cannot disagree with the program that has to obey
// it.
package setup

// Fact is one thing that can stand between this machine and an answer.
type Fact string

const (
	FactEngine  Fact = "engine"
	FactDevice  Fact = "device"
	FactModels  Fact = "models"
	FactServing Fact = "serving"
)

// Input is what the rule reads. Everything in it is a fact the caller already has;
// the package asks for nothing, so it can be tested against a machine that has no
// engine, no models and no browser.
type Input struct {
	// Managed says whether libris runs the model server. In external mode there is
	// nothing here to install, confirm or start, and libris cannot tell whether
	// somebody else's servers are up — so the honest answer about that setup is
	// "there is nothing to set up", not a wizard describing absent things.
	Managed bool

	EngineInstalled bool

	// Ack is what the person confirmed; AckNow is what confirming would mean today.
	// They are compared rather than stored as a yes, because "confirmed" is a claim
	// about an engine build on a machine: a new build reports a different device list,
	// and a data directory copied onto other hardware has never been shown this one.
	//
	// Blank Ack means nobody has been asked yet, which is a different state from
	// "libris picked a device" — and both leave EngineDevice blank, which is exactly why
	// the confirmation had to stop being inferable from that setting.
	Ack    string
	AckNow string

	ChatComplete  bool
	EmbedComplete bool

	Serving bool

	// Autostart says libris's own start-up has taken responsibility for bringing the
	// router up in this process. It is what turns "the process is the only thing
	// missing" into "the process is on its way": without it, a machine that finished
	// its steps part-way through a session would be let into the app with no router,
	// no start in flight, and the one button that starts it gone with the wizard.
	Autostart bool
}

// View is the answer, in the shape a caller uses.
//
// It is a view rather than a bool because a person reading the screen needs the next
// thing to do, not the fact that there is something to do — and because the start-up
// path needs to see that the only thing missing is the process it is about to make.
type View struct {
	// Ready means a question would be answered right now.
	Ready bool `json:"ready"`

	// Step is the first thing to do about it, for a screen that shows one step at a
	// time. Empty when there is nothing left to do.
	Step string `json:"step,omitempty"`

	// The four facts, so the screen can render all of them — including the ones after
	// the current step, which are listed as waiting rather than hidden.
	Engine  bool `json:"engine"`
	Device  bool `json:"device"`
	Models  bool `json:"models"`
	Serving bool `json:"serving"`

	// Blocking is what stands in the way, in the order a person would fix it. One
	// entry — serving — is the state libris can fix by itself, and start-up reads it
	// to decide whether it should.
	Blocking []Fact `json:"blocking,omitempty"`

	// Starting is that same single entry read the other way: the person has nothing
	// left to do and the router is the only thing missing, so the thing to look at is
	// the app with the server arriving behind it, not a wizard describing a wait.
	// It is a separate answer from Ready — "a question would be answered right now"
	// is still false while the model loads — and it is in the view rather than a
	// second rule in the screen, which is what the one-rule decision was about.
	Starting bool `json:"starting"`
}

// For computes the answer. Pure: nothing is asked of the world, and nothing is
// remembered.
func For(in Input) View {
	if !in.Managed {
		return View{
			Ready: true, Engine: true, Device: true, Models: true, Serving: true,
		}
	}

	engine := in.EngineInstalled
	// An unconfirmed device step is only a step once there is an engine to report
	// devices: a machine with no engine cannot answer the question yet either, and
	// the engine is what a person would act on first.
	device := engine && in.Ack != "" && in.Ack == in.AckNow
	models := in.ChatComplete && in.EmbedComplete
	serving := in.Serving

	v := View{Engine: engine, Device: device, Models: models, Serving: serving}

	// The facts in the order a person would fix them. This one list decides both what is
	// standing in the way and which thing to ask about first, so the order the screen
	// shows and the order start-up reads cannot drift apart.
	for _, f := range []struct {
		fact   Fact
		missed bool
	}{
		{FactEngine, !engine},
		{FactDevice, engine && !device},
		{FactModels, !models},
		{FactServing, !serving},
	} {
		if f.missed {
			v.Blocking = append(v.Blocking, f.fact)
		}
	}

	// The head of that list is the step, except when the only thing missing is serving —
	// libris's own job, which no person has to act on. That exception is also the only
	// case Starting can mean anything: nothing left to ask, and a start either in flight
	// or not.
	switch {
	case len(v.Blocking) == 1 && v.Blocking[0] == FactServing:
		v.Starting = in.Autostart
	case len(v.Blocking) > 0:
		v.Step = string(v.Blocking[0])
	}

	v.Ready = len(v.Blocking) == 0
	return v
}

// Startable says the only thing missing is the model server itself. It is the
// start-up path's whole question: bring the router up when everything else about
// this machine is already settled, and stay out of the way otherwise — a router
// started before anyone confirmed the device, or holding one of two models, would
// be a process that reports itself healthy and fails the next question.
//
// It is read from the same Blocking list the screen renders, because the program that
// starts the process and the page that waits for it are asking one question. It ignores
// Autostart on purpose: start-up is what sets that flag, so answering this one from it
// would let the flag decide whether to set itself. Unmanaged mode needs no clause —
// For has already said there is nothing blocking, so nothing is missing.
func Startable(in Input) bool {
	if in.Managed && !in.Serving {
		v := For(in)
		return len(v.Blocking) == 1 && v.Blocking[0] == FactServing
	}
	return false
}
