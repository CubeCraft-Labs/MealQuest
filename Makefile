# MealQuest build entry points.
#
#   make dev     Go server on :8080 plus Vite dev server on :5173 (proxies
#                /api, /auth, /media to :8080). Needs the MEALQUEST_* env set.
#   make test    gofmt check, go vet, go test -race, npm typecheck and test.
#   make build   Build the PWA, embed it, build the static binary bin/mealquest.
#   make docker  Build the container image from ./Dockerfile.

FRONTEND_DIST := internal/frontend/dist
# Dirty means a tracked change outside the generated embed output, which
# `make embed` rewrites; plain `--dirty` would flag every rebuild.
GIT_DIRTY := $(shell git status --porcelain --untracked-files=no -- . ':(exclude)$(FRONTEND_DIST)' 2>/dev/null)
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)$(if $(GIT_DIRTY),-dirty)
IMAGE ?= mealquest:$(VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: dev test go-test web-test build web embed check-embed-clean docker

# Run both servers; Ctrl-C (or either exiting) stops both. The binary is built
# first and run directly because `go run` would orphan its child on kill.
dev: SHELL := /bin/bash
dev:
	go build -ldflags "$(LDFLAGS)" -o bin/mealquest-dev ./cmd/server
	@trap 'kill $$(jobs -p) 2>/dev/null; wait' EXIT; \
	bin/mealquest-dev & \
	(cd web && exec npm run dev) & \
	wait -n

test: go-test web-test

go-test:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go test -race ./...

web-test:
	cd web && npm ci && npm run typecheck && npm test

build: embed
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/mealquest ./cmd/server

web:
	cd web && npm ci && npm run build

# Replace the committed placeholder with the built PWA so go:embed picks it up.
# Recreate the whole directory: a glob would skip dotfiles, which all:dist embeds.
embed: web
	rm -rf $(FRONTEND_DIST)
	mkdir -p $(FRONTEND_DIST)
	cp -R web/dist/. $(FRONTEND_DIST)/

# Regression check: a stale hidden file must not survive an embed.
check-embed-clean:
	touch $(FRONTEND_DIST)/.stale-embed-check
	$(MAKE) embed
	test ! -e $(FRONTEND_DIST)/.stale-embed-check

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .
