GO      ?= go
PKGS    := ./...

.PHONY: fmt vet test cover check generate build run web web-check ui clean

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

# Drives the real UI in headless Chrome against a throwaway data directory and
# asserts on DOM state. Skips (successfully) when Chrome is not installed.
UI_PORT ?= 8099
ui: build
	@dir=$$(mktemp -d); \
	./libris -addr 127.0.0.1:$(UI_PORT) -data $$dir >/tmp/libris-ui.log 2>&1 & pid=$$!; \
	sleep 1; \
	APP=http://127.0.0.1:$(UI_PORT)/ CDP_PORT=9321 node scripts/ui-upload.mjs; a=$$?; \
	APP=http://127.0.0.1:$(UI_PORT)/ CDP_PORT=9322 node scripts/ui-drop.mjs; b=$$?; \
	kill $$pid 2>/dev/null; rm -rf $$dir; \
	test $$a -eq 0 -a $$b -eq 0

clean:
	rm -f cover.out
