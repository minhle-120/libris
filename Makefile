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
# chat, which is also what the settings a user saves look like.
UI_PORT ?= 8099
UI_EMBED_PORT ?= 8124
ui: build
	@dir=$$(mktemp -d); \
	printf '{"chatURL":"http://127.0.0.1:$(UI_EMBED_PORT)","embedURL":"http://127.0.0.1:$(UI_EMBED_PORT)","rerankURL":"http://127.0.0.1:$(UI_EMBED_PORT)","embedModel":"fake-embed-2"}' > $$dir/settings.json; \
	node scripts/fake-embed.mjs $(UI_EMBED_PORT) >/tmp/libris-embed.log 2>&1 & embed=$$!; \
	sleep 0.4; \
	./libris -addr 127.0.0.1:$(UI_PORT) -data $$dir >/tmp/libris-ui.log 2>&1 & pid=$$!; \
	sleep 1; \
	APP=http://127.0.0.1:$(UI_PORT)/ EMBED=http://127.0.0.1:$(UI_EMBED_PORT) CDP_PORT=9321 node scripts/ui-upload.mjs; a=$$?; \
	APP=http://127.0.0.1:$(UI_PORT)/ CDP_PORT=9322 node scripts/ui-drop.mjs; b=$$?; \
	APP=http://127.0.0.1:$(UI_PORT)/ EMBED=http://127.0.0.1:$(UI_EMBED_PORT) CDP_PORT=9323 node scripts/ui-ask.mjs; c=$$?; \
	kill $$pid $$embed 2>/dev/null; rm -rf $$dir; \
	test $$a -eq 0 -a $$b -eq 0 -a $$c -eq 0

clean:
	rm -f cover.out
