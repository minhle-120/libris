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
# server is one port because one llama.cpp binary serves embeddings, reranking and
# chat, which is also what the settings a user saves look like. The model names are in
# that file because Libris ships no guesses: a blank chat model is a question that
# correctly refuses to run, and the harness would be asserting that instead of the
# answer path.
UI_PORT ?= 8099
UI_EMBED_PORT ?= 8124
# The three harnesses in the order they run, each on its own CDP port. One app and one
# fake for all of them, because that is what a running Libris looks like. UI_HARNESS
# narrows it to one harness while developing — the whole suite is about a minute, so
# this is about noise and signal, not waiting.
UI_HARNESS ?= ui-upload ui-drop ui-ask ui-nav
ui: build
	@for h in $(UI_HARNESS); do test -f scripts/$$h.mjs || { echo "make ui: no harness named $$h" >&2; exit 2; }; done
	@dir=$$(mktemp -d); \
	printf '{"chatURL":"http://127.0.0.1:$(UI_EMBED_PORT)","embedURL":"http://127.0.0.1:$(UI_EMBED_PORT)","rerankURL":"http://127.0.0.1:$(UI_EMBED_PORT)","chatModel":"fake-chat","embedModel":"fake-embed-2","rerankModel":"fake-rerank"}' > $$dir/settings.json; \
	node scripts/fake-embed.mjs $(UI_EMBED_PORT) >/tmp/libris-embed.log 2>&1 & embed=$$!; \
	sleep 0.4; \
	./libris -addr 127.0.0.1:$(UI_PORT) -data $$dir >/tmp/libris-ui.log 2>&1 & pid=$$!; \
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

clean:
	rm -f cover.out
