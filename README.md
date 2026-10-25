# MealQuest

MealQuest is a mobile-first Progressive Web App for weekly dinner planning for
one household. The household lists the dinners it makes regularly, an AI
assistant suggests new dinners to try (swipe "Try it" or "Pass"), and the app
builds a weekly plan, pushes it to Google Calendar, and generates a shopping
list grouped by dinner, kept as a browsable weekly history. It ships as a
single Go service with the React PWA embedded in the binary.

Production runs at <https://mealquest.cubecraftlabs.com> for one household,
signed in through Authentik.

## Stack

- **Backend:** Go 1.25 (`CGO_ENABLED=0`, one static binary), chi router,
  SQLite through `modernc.org/sqlite` with sqlc queries and embedded
  migrations, Authentik OIDC, `log/slog`. AI calls go to an
  OpenAI-compatible, Anthropic or Ollama endpoint over plain HTTP; recipes
  come from SearXNG or Brave search and the recipe page's schema.org data.
  Google Calendar, Google Chat and Web Push are the integrations.
- **Frontend:** React 19, TypeScript, Vite, React Router 7, TanStack Query 5,
  `vite-plugin-pwa`, CSS Modules over the design tokens; tests with Vitest,
  Testing Library and jsdom.
- **Deployment:** a Docker Compose stack (the service, Litestream
  replication to the NAS, a SearXNG sidecar) behind Traefik, with systemd
  units for the firewall, nightly media backup and the automated deploy of
  `main`.

The approved dependency list is in [AGENTS.md](AGENTS.md); nothing else is
added without the owner's approval.

## Running it locally

The developer setup (prerequisites, Authentik and Google clients, the
`MEALQUEST_*` environment, `make dev` and the optional SearXNG sidecar) is
runbook information and lives in CTXD: project `mealquest`, file
`RUNBOOKS.MD`, section "Local development". Configuration is read only from
the environment, and the service refuses to start, naming every missing or
invalid variable.

## Common commands

| Command        | What it does                                                    |
| -------------- | --------------------------------------------------------------- |
| `make dev`     | Go server plus Vite dev server.                                 |
| `make test`    | `gofmt` check, `go vet`, `go test -race`, web typecheck and tests. |
| `make build`   | Build the PWA, embed it, build the static binary `bin/mealquest`. |
| `make docker`  | Build the container image from the root `Dockerfile`.           |
| `make sqlc-check` | Regenerate `internal/db` with sqlc and fail if it drifted.   |

## Deploying and operating

[deploy/RUNBOOK.md](deploy/RUNBOOK.md) is the deploy runbook: build, ship and
restart, verifying the deployed version, rollback and the automated deploy.
Database restore, the boot configuration and other host procedures are in
CTXD: project `mealquest`, file `RUNBOOKS.MD`. Only `overseer` releases, by
merging `dev` into `main`.

## Where to read next

- [AGENTS.md](AGENTS.md) — house rules, approved dependencies, the quality
  floor every change must pass, repository layout, conventions.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — architecture of record.
- [design/MealQuest Requirements.md](design/MealQuest%20Requirements.md) — product requirements.
- [design/MealQuest UI Designs/design_handoff_mealquest/README.md](design/MealQuest%20UI%20Designs/design_handoff_mealquest/README.md) — UI handoff.
