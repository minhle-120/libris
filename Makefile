GO      ?= go
PKGS    := ./...

.PHONY: fmt vet test cover check generate build run web web-check ui e2e clean

fmt:
	$(GO) fmt $(PKGS)

generate:
	$(GO) generate ./internal/config

vet:
	$(GO) vet $(PKGS)

test:
	$(GO) test $(PKGS) -race -shuffle=on -timeout 120s

cover:
	$(GO) test $(PKGS) -covermode=atomic -coverprofile=cover.out -timeout 120s
	$(GO) tool cover -func=cover.out | tail -1

check: fmt vet test

build: web
	$(GO) build -trimpath -o libris ./cmd/libris

run:
	$(GO) run ./cmd/libris

web:
	cd web && npm run build

web-check:
	cd web && npx svelte-check --output human && npm run build

# Uploads to a running-out-of-a-throwaway-directory libris against a fake
# embeddings server that speaks llama.cpp's OpenAI shape. Asserts the probe
# discovers the dimension, that a dead server fails rather than readies a source,
# and that the vectors the API claims are actually rows.
e2e: build
	node scripts/e2e-embed.mjs

# Drives the real UI in headless Chrome against a throwaway data directory and
# asserts on DOM state. Skips (successfully) when Chrome is not installed. The fake
# server is one port because one llama.cpp binary serves embeddings and
# chat, which is also what the settings a user saves look like. The model names are in
# that file because Libris ships no guesses: a blank chat model is a question that
# correctly refuses to run, and the harness would be asserting that instead of the
# answer path.
UI_PORT ?= 8099
UI_EMBED_PORT ?= 8124
# The harnesses in the order they run, each on its own CDP port. One app and one fake
# for all of them, because that is what a running Libris looks like. UI_HARNESS narrows
# it to one harness while developing — the whole suite is about a minute, so this is
# about noise and signal, not waiting.
#
# ui-clips last and on purpose: it uploads a recording, which is the only thing in the
# suite that runs ffmpeg, so a machine without it fails one named harness rather than
# blurring a check on something else's row.
UI_HARNESS ?= ui-upload ui-drop ui-ask ui-nav ui-clips
ui: build
	@for h in $(UI_HARNESS); do test -f scripts/$$h.mjs || { echo "make ui: no harness named $$h" >&2; exit 2; }; done
# -open=false: a harness is headless by definition, and on a machine that happens to
# have a desktop the default would raise a browser window mid-run.
	@dir=$$(mktemp -d); \
	printf '{"engineMode":"external","chatURL":"http://127.0.0.1:$(UI_EMBED_PORT)","embedURL":"http://127.0.0.1:$(UI_EMBED_PORT)","chatModel":"fake-chat","embedModel":"fake-embed-2"}' > $$dir/settings.json; \
	node scripts/fake-embed.mjs $(UI_EMBED_PORT) >/tmp/libris-embed.log 2>&1 & embed=$$!; \
	sleep 0.4; \
	./libris -open=false -addr 127.0.0.1:$(UI_PORT) -data $$dir >/tmp/libris-ui.log 2>&1 & pid=$$!; \
	sleep 1; \
	rc=0; i=0; \
	for h in $(UI_HARNESS); do \
		i=$$((i+1)); \
		APP=http://127.0.0.1:$(UI_PORT)/ EMBED=http://127.0.0.1:$(UI_EMBED_PORT) \
			CDP_PORT=$$((9320+i)) node scripts/$$h.mjs || rc=1; \
	done; \
	kill $$pid $$embed 2>/dev/null; rm -rf $$dir; \
	test $$i -gt 0 || { echo "make ui: no harness ran" >&2; exit 2; }; \
	test $$rc -eq 0

# One harness, same fixture: make ui-ask, ui-drop, ui-upload or ui-nav.
ui-upload ui-drop ui-ask ui-nav:
	@$(MAKE) --no-print-directory ui UI_HARNESS=$@

# The first-run gate needs a real binary on an empty data directory: make ui's fixture is
# deliberately external-mode, where there is nothing to install, no device to pick, and no
# model to fetch — so the gate must not appear there either, which is the other half of it.
# The empty directory is the first run, and the network is used for one thing: asking upstream
# which release to offer. READY=1 pointed at a machine that has everything asserts the reverse.
ui-setup: build
	@dir=$$(mktemp -d); \
	./libris -open=false -addr 127.0.0.1:$(UI_PORT) -data $$dir >/tmp/libris-setup.log 2>&1 & pid=$$!; \
	sleep 1; \
	APP=http://127.0.0.1:$(UI_PORT)/ CDP_PORT=9329 node scripts/ui-setup.mjs; rc=$$?; \
	kill $$pid 2>/dev/null; rm -rf $$dir; \
	test $$rc -eq 0

# The sentence beside a PDF that no program on this machine could read. Which sentence is right
# depends on whether this install brought its own folder of programs, so the fixture has to make
# both true: PATH=/nonexistent is a machine with nothing installed, and the binary is copied into
# a throwaway directory so the folder beside it can be made to exist or not — the checkout must
# not grow a tools folder in order to be tested. Both halves run against the same code path, one
# with the folder and one without.
ui-tools: build
	@i=0; for kind in bundle install; do \
		i=$$((i+1)); port=$$(( $(UI_PORT) + i )); \
		dir=$$(mktemp -d); \
		cp libris $$dir/libris; \
		if test $$kind = bundle; then mkdir -p $$dir/tools; fi; \
		mkdir -p $$dir/data; \
		printf '%s' '{"engineMode":"external","chatURL":"http://127.0.0.1:9/none","embedURL":"http://127.0.0.1:9/none","chatModel":"none","embedModel":"none"}' > $$dir/data/settings.json; \
		PATH=/nonexistent $$dir/libris -open=false -addr 127.0.0.1:$$port \
			-data $$dir/data >/tmp/libris-tools-$$kind.log 2>&1 & pid=$$!; \
		sleep 1; \
		EXPECT=$$kind APP=http://127.0.0.1:$$port/ CDP_PORT=$$((9330 + i)) \
			node scripts/ui-tools.mjs; rc=$$?; \
		kill $$pid 2>/dev/null; wait $$pid 2>/dev/null; \
		rm -rf $$dir; \
		test $$rc -eq 0 || exit 1; \
	done

clean:
	rm -f cover.out
