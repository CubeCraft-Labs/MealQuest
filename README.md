# MealQuest

MealQuest is a mobile-first Progressive Web App for weekly dinner planning for
one household. The household lists the dinners it makes regularly, an AI
assistant suggests new dinners to try (swipe "Try it" or "Pass"), and the app
builds a weekly plan, pushes it to Google Calendar, and generates a shopping
list grouped by dinner, kept as a browsable weekly history. It ships as a
single Go service with the React PWA embedded in the binary.

> Status: this repository is the Feature 01 scaffold (Go service with
> `/health`, embedded PWA skeleton, Makefile, Dockerfile, CI). The planned
> application is described in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start

Requirements: Go 1.25 and Node.js `^22.22.2 || ^24.15.0 || >=26.0.0` — that
is, 22.22.2 or later on the 22 line, 24.15.0 or later on the 24 line, or 26.0.0
or later. Node.js 23, 24.0–24.14 and 25 are not supported: this is the
`engines` range of jsdom in the committed lockfile, the strictest of all the
web dependencies.

```sh
(cd web && npm ci)
mkdir -p ~/.mealquest-dev   # outside the repository: it holds a secret
chmod 700 ~/.mealquest-dev
head -c 32 /dev/urandom | base64 > ~/.mealquest-dev/secret_key
chmod 600 ~/.mealquest-dev/secret_key
export MEALQUEST_HTTP_ADDR=:8080
export MEALQUEST_PUBLIC_URL=http://localhost:5173
export MEALQUEST_DATA_DIR="$HOME/.mealquest-dev"
export MEALQUEST_SHUTDOWN_TIMEOUT=10s
export MEALQUEST_SECRET_KEY_FILE="$HOME/.mealquest-dev/secret_key"
# Authentik OAuth2/OpenID provider for MealQuest (redirect URI:
# $MEALQUEST_PUBLIC_URL/auth/callback). Save its client secret to the file.
export MEALQUEST_OIDC_ISSUER=https://auth.example.com/application/o/mealquest/
export MEALQUEST_OIDC_CLIENT_ID=mealquest
export MEALQUEST_OIDC_CLIENT_SECRET_FILE="$HOME/.mealquest-dev/oidc_client_secret"
export MEALQUEST_OIDC_ALLOWED_GROUPS=mealquest-admin
make dev
```

`make dev` runs the Go server on `:8080` and the Vite dev server on `:5173`
(which proxies `/api`, `/auth` and `/media` to `:8080`). Open
http://localhost:5173. Configuration is environment-only and fails fast:
every variable above is required, and secrets are read only from files named
by `*_FILE` variables. `MEALQUEST_PUBLIC_URL`, `MEALQUEST_OIDC_ISSUER` and the
endpoints the issuer advertises must be `https://`; plain `http://` is
accepted only for `localhost`, `127.0.0.1` and `[::1]`.

## Common commands

| Command        | What it does                                                    |
| -------------- | --------------------------------------------------------------- |
| `make dev`     | Go server plus Vite dev server.                                 |
| `make test`    | `gofmt` check, `go vet`, `go test -race`, web typecheck and tests. |
| `make build`   | Build the PWA, embed it, build the static binary `bin/mealquest`. |
| `make docker`  | Build the container image from the root `Dockerfile`.           |

## Where to read next

- [AGENTS.md](AGENTS.md) — house rules, approved dependencies, conventions.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — architecture of record.
- [design/MealQuest Requirements.md](design/MealQuest%20Requirements.md) — product requirements.
- [design/MealQuest UI Designs/design_handoff_mealquest/README.md](design/MealQuest%20UI%20Designs/design_handoff_mealquest/README.md) — UI handoff.
