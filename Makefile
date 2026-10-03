# Build entry point: build the PWA, embed it, then build the server binary.

FRONTEND_DIST := internal/frontend/dist

.PHONY: build web embed check-embed-clean

build: embed
	go build -o bin/mealquest ./cmd/server

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
