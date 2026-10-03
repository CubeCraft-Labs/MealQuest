# Build entry point: build the PWA, embed it, then build the server binary.

FRONTEND_DIST := internal/frontend/dist

.PHONY: build web embed

build: embed
	go build -o bin/mealquest ./cmd/server

web:
	cd web && npm ci && npm run build

# Replace the committed placeholder with the built PWA so go:embed picks it up.
embed: web
	rm -rf $(FRONTEND_DIST)/*
	cp -R web/dist/. $(FRONTEND_DIST)/
