# AGENTS.md — MealQuest agent guide

This is the authoritative guide for every agent (and human) working in this
repository. `CLAUDE.md` only imports this file. Where this guide and a general
habit disagree, this guide wins.

MealQuest is a single Go service with an embedded React PWA for one
household. The architecture of record is [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md);
product requirements are in [design/MealQuest Requirements.md](design/MealQuest%20Requirements.md).

## House rules

1. **Fail fast.** Invalid configuration, input or state is an error reported
   immediately and clearly — the service refuses to start on bad config.
2. **No silent fallbacks.** Never substitute a default, retry quietly, or
   swallow an error to "keep going". If something is missing, say so.
3. **One way to do a thing.** No parallel code paths, alternative settings or
   "just in case" options. Pick one approach and use it everywhere.
4. **No new third-party dependency without owner approval.** Use only the
   approved list below; anything else needs the owner's explicit yes first.
5. **Never use a human owner's forge credentials.** Agents act only through
   their own GitHub identities.

## Approved dependencies (from CTXD)

Backend:

- Go 1.25, built with `CGO_ENABLED=0`.
- `github.com/go-chi/chi/v5` — HTTP router.
- `modernc.org/sqlite` — CGO-free SQLite driver.
- `sqlc` — query code generation (dev tool only).
- `github.com/coreos/go-oidc/v3` — OIDC (Authentik).
- `golang.org/x/oauth2` and `golang.org/x/oauth2/google`.
- `github.com/SherClockHolmes/webpush-go` — Web Push.
- `golang.org/x/image` — photo processing.
- `github.com/oklog/ulid/v2` — IDs.
- Standard library `log/slog` and `net/http`.
- **Not allowed:** LLM vendor SDKs, Google API SDKs (call the HTTP APIs
  directly), goose, golang-migrate.

Frontend:

- React 19, TypeScript, Vite.
- React Router 7, TanStack Query 5.
- `vite-plugin-pwa`.
- `lucide-react` icons at stroke width 2.75.
- CSS Modules over the design tokens in
  [tokens.css](design/MealQuest%20UI%20Designs/design_handoff_mealquest/tokens.css).
  **No Tailwind, no Framer Motion, no hard-coded hex colours.**
- Self-hosted fonts: Bagel Fat One and Figtree.
- Tests: Vitest, Testing Library, jsdom.

## Quality floor

Every change must pass all of these before review (CI in
[.github/workflows/ci.yml](.github/workflows/ci.yml) enforces them):

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race -p 2 -timeout 30m ./...
cd web && npm run typecheck && npm test && npm run build
```

And the build entry points in the [Makefile](Makefile) and [Dockerfile](Dockerfile):

```sh
make test
make build
docker build .
```

Lint proves code parses, not that it works: where a change has runtime
behaviour, exercise it (hit the endpoint, render the page).

## Repository layout

| Path | Purpose |
| --- | --- |
| `cmd/server` | Entry point. Wiring only — no business logic. |
| `cmd/vapidgen` | Prints a new VAPID key pair for Web Push. |
| `internal/config` | `MEALQUEST_*` environment config; secrets from `*_FILE`. |
| `internal/server` | HTTP server, routes, `/health`. |
| `internal/frontend` | `go:embed` of the built PWA with SPA fallback. |
| `internal/*` | `auth`, `forwarded`, `secureurl`, `db`, `store`, `secretbox`, `household`, `dinners`, `recipes`, `ai/llm`, `ai/harness`, `ai/skills`, `ai/aisettings`, `suggest`, `plan`, `shopping`, `ideas`, `home`, `gcal`, `notify`, `push`, `chat`, `vapid`, `jobs`, `scheduler` — one line each in [ARCHITECTURE.md](docs/ARCHITECTURE.md#repository-layout). |
| `migrations/` | SQL migrations, embedded and applied at boot by `internal/store`; sqlc queries in `migrations/queries/`. |
| `web/` | React PWA source. |
| `deploy/` | Deployment files: compose stack, systemd units, [deploy runbook](deploy/RUNBOOK.md) and the automated deploy (`auto-deploy.sh` with `systemd/mealquest-auto-deploy.*`). |
| `docs/` | Project documentation, including [ARCHITECTURE.md](docs/ARCHITECTURE.md), launch evidence in [LAUNCH.md](docs/LAUNCH.md), and Lighthouse reports in `docs/lighthouse/`. |
| `design/` | Requirements and UI handoff. **Read-only reference** — do not edit. |
| `Dockerfile` | Multi-stage image build, at the repository root. |
| `Makefile` | `dev`, `test`, `build`, `docker`; `sqlc` and `sqlc-check` regenerate and check `internal/db`. |
| `.github/workflows/ci.yml` | GitHub Actions CI quality floor and production deploy gate. |

Design references:

- [design/MealQuest Requirements.md](design/MealQuest%20Requirements.md)
- [design/MealQuest UI Designs/design_handoff_mealquest/README.md](design/MealQuest%20UI%20Designs/design_handoff_mealquest/README.md)
- [design/MealQuest UI Designs/design_handoff_mealquest/tokens.css](design/MealQuest%20UI%20Designs/design_handoff_mealquest/tokens.css)

## Branches, pull requests and merges

- Integration branch is `dev`. `main` is the release branch.
- **Feature branches:** `feature/<feature-uuid>-<slug>`, branched from `dev`.
- **Task branches:** `task/<slug>_<task-uuid>`, branched from the feature branch.
- Task pull requests **squash-merge into the feature branch**.
- The feature pull request merges into `dev`.
- **Only `overseer` merges `dev` → `main`** (a release). On the production
  host, `mealquest-auto-deploy.timer` then deploys the new `main` once its
  CI passes ([deploy/RUNBOOK.md](deploy/RUNBOOK.md) "Automated deploys");
  nothing deploys `dev` or a feature branch.
- Never push directly to `dev` or `main`.
- Authors never review, approve, merge or close their own work.
- Never store credentials in the repository; environment or ignored files only.

## Where context lives and where work is tracked

- **Context:** CTXD project `mealquest` — client guide at
  <https://ctxd.cubecraftcreations.com>. Project documents: `CONTEXT.MD`
  (architecture), `DECISIONS.MD` (decision log, e.g. D-021), `RUNBOOKS.MD`,
  `PROMPTS.MD`, `GLOSSARY.MD`.
- **Work tracking:** Maestro at <https://maestro.cubecraftlabs.com>,
  workspace **Maniacs-Mansion** → project **MealQuest**. Every change has a
  task with a verifiable done-condition.

## Vocabulary (GLOSSARY)

Use these product terms in code names, UI copy, docs and commit messages. Do
not invent synonyms.

| Term | Meaning |
| --- | --- |
| **Regulars** | Dinners the household makes routinely. |
| **Side quest** | An AI-suggested new dinner to try. |
| **Quest log** | The to-try pool of accepted Side quests, scheduled later. |
| **Try it / Pass** | Accept (swipe right) / decline (swipe left) a suggestion. |
| **Rotation check** / **"Keep it in rotation?"** | The prompt after a Quest-log dinner is tried, asking whether it becomes a Regular. Promotion is always the household's choice, never automatic. |
| **Supply run** | The current week's shopping list, grouped by dinner. |
| **Supply archive** | The blog-style history of past shopping lists (by week, month, year). |
| **Draft board** | The mobile weekly plan layout the AI drafts for the household to adjust. |
| **Rebuild week** | Ask the AI to redraft the week's plan. |
| **Push to Calendar** | Push the plan one-way to Google Calendar. |
| **Quick dinner** | Cooked in a crock pot **or** 30 minutes or less from prep to table. |
| **Cook from what I have** | Ideas from a list of ingredients on hand. |
| **Kitchen reading mode** | Tablet recipe view: large text, ingredient checkboxes, step-by-step. |
| **"Why this?"** | The reason shown for a suggestion, referencing the household's Regulars. |
| **Our kitchen / Shared household** | The single shared household identity; no per-person profiles. |
