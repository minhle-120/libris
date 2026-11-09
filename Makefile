GO      ?= go
PKGS    := ./...

.PHONY: fmt vet test cover check generate web web-check

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

web:
	cd web && npm run build

web-check:
	cd web && npm run build

clean:
	rm -f cover.out
