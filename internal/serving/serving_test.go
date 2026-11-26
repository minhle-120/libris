package serving

import (
	"errors"
	"testing"

	"libris/internal/config"
	"libris/internal/engine"
)

// fakeServer is the supervisor as this package sees it: a status, on demand.
type fakeServer struct{ s engine.Serving }

func (f fakeServer) Status() engine.Serving { return f.s }

func managed(port int) config.Config {
	c := config.Default()
	c.EnginePort = port
	return c
}

func TestEndpointsDerivesManagedMode(t *testing.T) {
	cfg := managed(9099)
	if cfg.EngineMode != config.EngineManaged {
		t.Fatalf("a new install should default to managed, got %q", cfg.EngineMode)
	}
	cfg.ChatModel = "something-somebody-typed"

	got, err := Endpoints(fakeServer{engine.Serving{Running: true, Port: 9099}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{got.ChatURL, got.EmbedURL} {
		if u != "http://127.0.0.1:9099" {
			t.Errorf("managed URL = %q, want the loopback port libris binds", u)
		}
	}
	if got.ChatModel != "libris-chat" || got.EmbedModel != "libris-embed" {
		t.Errorf("managed models = %q/%q", got.ChatModel, got.EmbedModel)
	}
	// A question asks the embedding model's other instance, the one off the card. Two names for
	// one file is the whole arrangement, and the preset libris wrote is what makes the second one
	// answerable — which is why it exists only here.
	if got.EmbedQueryModel != "libris-embed-cpu" {
		t.Errorf("managed mode asks a question through %q", got.EmbedQueryModel)
	}
	if !got.Live || got.Port != 9099 || got.Advertised != "http://127.0.0.1:9099" {
		t.Errorf("view fields = live:%v port:%d advertised:%q", got.Live, got.Port, got.Advertised)
	}
	// The stored config is untouched: a derived value that lands in settings.json is a
	// second source of truth that goes stale the moment the port changes.
	if cfg.ChatURL == got.ChatURL || cfg.ChatModel == got.ChatModel {
		t.Error("Endpoints wrote derived values back into the settings")
	}
}

// External mode is the identity. The whole point of the switch is that the fields a person
// typed are used exactly as typed — including by a machine that has no router of its own.
func TestEndpointsIsTheSettingsInExternalMode(t *testing.T) {
	cfg := managed(0)
	cfg.EngineMode = config.EngineExternal
	cfg.ChatURL = "http://elsewhere:9/v1"
	cfg.EmbedURL = "http://elsewhere:9/v1"
	cfg.ChatModel = "someone-elses-chat"
	cfg.EmbedModel = "someone-elses-embed"

	got, err := Endpoints(nil, cfg)
	if err != nil {
		t.Fatalf("external mode should never report a stopped router: %v", err)
	}
	if got.ChatURL != cfg.ChatURL || got.EmbedURL != cfg.EmbedURL {
		t.Errorf("external addresses were rewritten: %+v", got)
	}
	if got.ChatModel != cfg.ChatModel || got.EmbedModel != cfg.EmbedModel {
		t.Errorf("external model names were replaced with aliases: %+v", got)
	}
	// One name, because there is one server and libris did not configure it. Inventing a second
	// alias here would send a question to a name that exists only in a preset nobody wrote.
	if got.EmbedQueryModel != cfg.EmbedModel {
		t.Errorf("external mode asks a question through %q, want the name the settings hold", got.EmbedQueryModel)
	}
	// libris does not know what runs behind an address it was handed, so it must not
	// claim "stopped" — a working external install would render as broken.
	if got.Live || got.Advertised != "" {
		t.Errorf("external mode claimed something is running: %+v", got)
	}
}

// Managed mode with nothing running is an error, not an empty address: the alternative is a
// socket error blaming a server that was never asked.
func TestEndpointsStoppedRouterIsAnErrorWithNoAddress(t *testing.T) {
	cfg := managed(8080) // stored, but nothing is bound to it
	got, err := Endpoints(fakeServer{engine.Serving{Running: false, Port: 8080}}, cfg)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
	for _, u := range []string{got.ChatURL, got.EmbedURL} {
		if u != "" {
			t.Errorf("a stopped router produced a dialable address: %q", u)
		}
	}
	// The names are still derived: they are facts about the preset, which exists whether or
	// not anything is reading it.
	if got.ChatModel != "libris-chat" || got.EmbedModel != "libris-embed" {
		t.Errorf("managed models = %q/%q", got.ChatModel, got.EmbedModel)
	}
	// And the page still gets the honest picture: not live, and here is where one would be.
	if got.Live || got.Port != 8080 || got.Advertised != "http://127.0.0.1:8080" {
		t.Errorf("view fields = live:%v port:%d advertised:%q", got.Live, got.Port, got.Advertised)
	}
}

// A status that reports a port without a live process is the case the two old copies of this
// rule disagreed about: the page required Running, the wiring did not, so the client could be
// aimed at a pid that had already died.
func TestEndpointsAPortIsNotARouter(t *testing.T) {
	cfg := managed(0)
	got, err := Endpoints(fakeServer{engine.Serving{Running: false, Port: 41234}}, cfg)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning for a port with no process", err)
	}
	if got.ChatURL != "" {
		t.Errorf("dialled a dead port: %q", got.ChatURL)
	}
	if got.Port != 0 || got.Advertised != "" {
		t.Errorf("Auto with nothing running should advertise nothing, got %+v", got)
	}
}

// Under Auto the stored port is 0 and the running router is on a number the kernel chose, so
// the live answer wins over the setting — and the page is told that same number, not the
// stored zero it used to show.
func TestEndpointsLivePortBeatsTheSetting(t *testing.T) {
	cfg := managed(0)
	got, err := Endpoints(fakeServer{engine.Serving{Running: true, Port: 41234}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatURL != "http://127.0.0.1:41234" {
		t.Errorf("managed URL = %q, want the port the running server reports", got.ChatURL)
	}
	if got.Port != 41234 || got.Advertised != "http://127.0.0.1:41234" {
		t.Errorf("the page was told %d / %q, want the live 41234", got.Port, got.Advertised)
	}
}

// A missing supervisor is a machine with no data directory to run one from: not running,
// rather than a nil dereference in the middle of a question.
func TestEndpointsWithoutAServer(t *testing.T) {
	if _, err := Endpoints(nil, managed(8080)); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestApplyReplacesOnlyWhatARequestReads(t *testing.T) {
	cfg := managed(9099)
	cfg.EmbedDim = "768"
	cfg.TimeoutSeconds = 42
	storedChatURL := cfg.ChatURL // what the settings file holds, in external mode's shape
	got, err := Endpoints(fakeServer{engine.Serving{Running: true, Port: 9099}}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	out := got.Apply(cfg)
	if out.ChatURL != "http://127.0.0.1:9099" || out.EmbedURL != out.ChatURL {
		t.Errorf("addresses = %q / %q", out.ChatURL, out.EmbedURL)
	}
	if out.ChatModel != "libris-chat" || out.EmbedModel != "libris-embed" {
		t.Errorf("models = %q/%q", out.ChatModel, out.EmbedModel)
	}
	// Everything a request does not dial with survives: this is a projection, not a rebuild.
	if out.EnginePort != cfg.EnginePort || out.EmbedDim != "768" || out.TimeoutSeconds != 42 {
		t.Errorf("Apply changed settings it has no business touching: %+v", out)
	}
	if cfg.ChatURL != storedChatURL {
		t.Errorf("Apply wrote back into the settings it was given: %q", cfg.ChatURL)
	}
}

func TestAliasesAreThePresetNames(t *testing.T) {
	got := Aliases()
	want := []string{"libris-chat", "libris-embed"}
	if len(got) != len(want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("alias %d = %q, want %q", i, got[i], want[i])
		}
	}
}
