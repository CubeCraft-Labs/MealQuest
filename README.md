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

## Where to read next

- [AGENTS.md](AGENTS.md) — house rules, approved dependencies, the quality
  floor every change must pass, repository layout, conventions.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — architecture of record.
- [design/MealQuest Requirements.md](design/MealQuest%20Requirements.md) — product requirements.
- [design/MealQuest UI Designs/design_handoff_mealquest/README.md](design/MealQuest%20UI%20Designs/design_handoff_mealquest/README.md) — UI handoff.
