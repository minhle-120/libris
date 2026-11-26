// Package serving answers one question, for everyone who wants to talk to a model:
// what do I dial, and what do I call the thing at the other end?
//
// It exists because that question has two very different answers depending on a
// setting nobody dials with. In external mode the addresses and model names are
// things a person typed, and the honest answer is "use them exactly as written".
// In managed mode they are facts about one llama.cpp process libris starts: one
// loopback address, a port the kernel may have chosen at start, and two names from
// the preset libris wrote. A settings file cannot hold any of that, so it must be
// derived — and derived the same way by the client that dials, the supplier that
// probes a vector length, the wiring that picks a model name, and the page that
// says where the router lives.
//
// Before this package, each of those asked a supervisor for a port and re-ran the
// arithmetic, three times, in three places, each with a comment explaining its own
// staleness rule. Two of those rules disagreed: the page refused to report a port
// unless the process was running, the wiring reported it either way. So the same
// fact was written three times and was already wrong in one of them.
//
// The rule the package states once: a running process is the answer; when nothing
// runs, managed mode has no address to dial, and says so as an error rather than as
// an empty string someone might send to a socket.
package serving

import (
	"errors"
	"libris/internal/config"
	"libris/internal/engine"
)

// Server is the process that might be answering, seen from below: the one thing
// this package needs from the supervisor, declared here rather than imported as a
// whole manager. Anything returning engine's own status satisfies it, so the wiring
// hands over the supervisor it already built and a test hands over a function.
//
// The status type is engine's because "which port, is it alive, which models" is
// engine's fact to report. Re-declaring it here would be a second place where
// "running" gets decided — which is the bug this package exists to prevent.
type Server interface {
	Status() engine.Serving
}

// ErrNotRunning is managed mode with no router answering. It travels as a value
// because the callers split in two: one group has to fail rather than invent an
// address (a client, a probe), and the other has to keep rendering — a settings
// page is more useful showing "stopped, and it would live here" than showing
// nothing. Both read the same error to tell which side they are on.
//
// It is not "the connection failed". A request that was never sent must not be
// reported as a server that refused one, which is the mistake this shape makes
// impossible: there is no address to try.
var ErrNotRunning = errors.New("serving: the model server is not running")

// Target is the answer to "what do I dial and what do I call it".
//
// The four dial fields are only meaningful when the error was nil. Live, Port and
// Advertised are meaningful either way, which is why they are fields rather than a
// second call: the page wants the same three facts a client wants, and a status
// read a moment apart from the dial fields would be a page that contradicts the
// request it is describing.
type Target struct {
	// ChatURL and EmbedURL are the same address in managed mode — one process holds
	// both models — and whatever the settings say in external.
	ChatURL  string
	EmbedURL string

	// ChatModel and EmbedModel are what the server answers to. In managed mode they
	// are the aliases libris wrote into the preset, never what the settings hold:
	// those fields are blank in a fresh install, or hold a name typed for somebody
	// else's server once.
	ChatModel  string
	EmbedModel string

	// EmbedQueryModel is the name a question's search vector asks for. Usually EmbedModel. In
	// managed mode it is not: the embedding model is loaded twice there, once on the chosen device
	// for the thousand vectors of an upload and once on the host for the one vector of a question,
	// and a loaded model cannot be asked to change device between the two.
	//
	// Nothing else about the embedding model changes. The vectors an upload makes and the vector a
	// question makes are recorded under the same model, because that is what a search filters on.
	EmbedQueryModel string

	// Live means a process is answering right now. False in external mode always —
	// libris does not know what runs behind an address a person typed, and guessing
	// would turn a working external install into a page that says "stopped".
	Live bool

	// Port is where the router answers, or where libris would bind one: the live
	// number when it is running, the setting when it is not, and zero for Auto with
	// nothing started — because zero is the truth in that case, and a fabricated
	// 8080 would be a claim about a process that does not exist.
	Port int

	// Advertised is the address the page shows: the live one when there is a
	// process, otherwise the one a start would use. Empty when that is unknowable,
	// which is external mode (no single address) and Auto with nothing running
	// (nothing has been given a port yet).
	Advertised string
}

// Endpoints resolves the settings into what to dial.
//
// It is a function rather than a struct because the package holds no state worth
// holding: the port belongs to the supervisor, the mode and typed values belong to
// the settings, and caching either would be a second answer to "what is running"
// with its own expiry rule. Callers that need to rebuild a client when the answer
// changes key themselves on the fields they dial — which is a staleness rule about
// their own object, and belongs with that object.
//
// The Target is useful even alongside ErrNotRunning: the dial fields are empty, so
// no socket can be aimed at a hole, and the display fields still say where the
// router would be.
func Endpoints(server Server, cfg config.Config) (Target, error) {
	if cfg.EngineMode != config.EngineManaged {
		return Target{
			ChatURL:         cfg.ChatURL,
			EmbedURL:        cfg.EmbedURL,
			ChatModel:       cfg.ChatModel,
			EmbedModel:      cfg.EmbedModel,
			EmbedQueryModel: cfg.EmbedModel,
		}, nil
	}

	t := Target{
		ChatModel:  string(engine.AliasChat),
		EmbedModel: string(engine.AliasEmbed),
		// A second instance of the embedding model, the one kept off the card, which is what a
		// question asks. Nothing about it is a model of its own: it is this file again, and it
		// exists only in a preset libris wrote — hence the external answer above, which is the
		// same name twice because somebody else's server has no second instance to offer.
		EmbedQueryModel: string(engine.AliasEmbedCPU),
	}

	port := 0
	if server != nil {
		if srv := server.Status(); srv.Running && srv.Port > 0 {
			port = srv.Port
			t.Live = true
		}
	}
	if !t.Live {
		// Not running. The setting is what a start would bind, which is a fact worth
		// showing and not a fact worth dialling.
		port = cfg.EnginePort
	}
	t.Port = port
	if port > 0 {
		t.Advertised = engine.Endpoint("127.0.0.1", port)
	}
	if !t.Live {
		return t, ErrNotRunning
	}
	t.ChatURL, t.EmbedURL = t.Advertised, t.Advertised
	return t, nil
}

// Apply is the settings as this router answers them: same struct, with the fields a
// model request reads replaced.
//
// The carrier stays config.Config because that is what the modules downstream take
// — a supplier keys its probe on a URL and a model name out of a settings value,
// and rewriting those seams is a different change than moving the derivation. What
// matters is who owns the derivation: a settings struct that quietly became a live
// address in one of its methods let a caller that skipped the method dial the stored
// value and get a confusing answer, so the replacement only happens through an
// answer someone had to ask for.
func (t Target) Apply(cfg config.Config) config.Config {
	out := cfg
	out.ChatURL, out.EmbedURL = t.ChatURL, t.EmbedURL
	out.ChatModel, out.EmbedModel = t.ChatModel, t.EmbedModel
	return out
}

// Aliases are the names libris gives its models in the preset, and therefore the
// names it sends. Listed once, in role order, because the settings page shows them
// as "what the router answers to" and a page that retypes them is a page that can
// drift from the preset that was written.
//
// One name per role, which is not every name the router answers to: the embedding model is
// served twice, and the second instance has no role because it has no download, no catalogue
// entry and nothing a person could choose. A page that listed it beside the other two would be
// offering a third something to configure when there are two things.
func Aliases() []string {
	return []string{string(engine.AliasChat), string(engine.AliasEmbed)}
}
