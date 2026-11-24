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
	if !engine {
		v.Step = string(FactEngine)
	} else if !device {
		v.Step = string(FactDevice)
	} else if !models {
		v.Step = string(FactModels)
	}

	if !engine {
		v.Blocking = append(v.Blocking, FactEngine)
	}
	if engine && !device {
		v.Blocking = append(v.Blocking, FactDevice)
	}
	if !models {
		v.Blocking = append(v.Blocking, FactModels)
	}
	if !serving {
		v.Blocking = append(v.Blocking, FactServing)
	}

	v.Ready = len(v.Blocking) == 0
	return v
}

// Startable says the only thing missing is the model server itself. It is the
// start-up path's whole question: bring the router up when everything else about
// this machine is already settled, and stay out of the way otherwise — a router
// started before anyone confirmed the device, or holding one of two models, would
// be a process that reports itself healthy and fails the next question.
func Startable(in Input) bool {
	if !in.Managed || in.Serving {
		return false
	}
	v := For(in)
	return len(v.Blocking) == 1 && v.Blocking[0] == FactServing
}
