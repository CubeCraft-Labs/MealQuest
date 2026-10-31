# MealQuest v1.0.0 release notes

MealQuest is a mobile-first Progressive Web App for one household's weekly
dinners, shipped as a single Go service with the React PWA embedded
([README.md](../README.md), [ARCHITECTURE.md](ARCHITECTURE.md)).

## What this release is

- **Code:** `dev` at `9a7aa12` ("Feature: UI Bugs (Maestro a1c87328) (#4)").
  Promotion pull request #10 (`release/2026-10-25`) merged it into `main`
  as `273cf8f`, whose tree is identical to `dev` at `9a7aa12`.
- **No `v1.0.0` tag exists yet.** The image reports its version from
  `git describe` (a release tag, else the short SHA), so until a tag is
  pushed, `/health` reports a short SHA
  ([deploy/RUNBOOK.md](../deploy/RUNBOOK.md) "How the image carries its
  version").
- **Deployed and verified:** production answered `GET /health` with
  `{"status":"ok","version":"561f249"}` (owner confirmation, 2026-10-25).
  `561f249` is the earlier `main` release of Feature 24 (pull request #211),
  so it **does not include the UI Bugs fixes**. These notes do not record
  whether `273cf8f` has been deployed since.

## After the v1.0.0 cut — in this tree

This tree also holds commits made after `main` `273cf8f`. They change how
CI runs and how the automated deploy decides whether to deploy. They are
not part of the v1.0.0 cut above:

- **CI on GitHub Actions** (`73e351f`, pull request #14): the CI workflow
  is now [.github/workflows/ci.yml](../.github/workflows/ci.yml). It runs
  on every push and pull request and calls `make test`,
  `make sqlc-check` and `make build`.
- **The deploy gate reads GitHub** (`7cea30e`, pull request #12, Maestro
  10a33c37): `deploy/auto-deploy.sh` reads the check runs and commit
  statuses that GitHub reports for the exact `origin/main` commit, from the
  public API.
  - It deploys only when every check succeeded.
  - While any check is pending, it waits and deploys nothing.
  - It refuses when a check failed or when the CI cannot be read.
  - It refuses a commit with no CI at all, unless the owner sets
    `MEALQUEST_DEPLOY_ALLOW_NO_CI=1` on the host. Any other value of that
    variable, including an empty one, stops the script at start.
  - Each deploy record line ends with `ci=success`,
    `ci=none(MEALQUEST_DEPLOY_ALLOW_NO_CI=1)` or `ci=not-checked(--ref)`.
    A manual `--ref` deploy bypasses the gate.

## What shipped

Features are grouped by area. Each one names the commit that brought it
into `dev` (`git log --oneline dev`).

### Foundations (Features 01–11)

- **Repository scaffold and CI** (Feature 01): Go module, fail-fast
  configuration, `/health`, the embedded PWA with SPA fallback, Makefile,
  multi-stage Dockerfile and CI workflow (`65b27a7`).
- **SQLite data layer and schema** (Feature 02): CGO-free SQLite, embedded
  migrations applied at boot, sqlc queries (`19ea8d5`).
- **Authentik OIDC sessions** (Feature 03): server-side sessions, CSRF
  protection, a household group gate and trusted-proxy handling (`41ff2ab`).
- **Platform services** (Feature 04): AES-256-GCM secrets at rest,
  household settings, household-local time and the async job runner
  (`7cc9612`).
- **Frontend foundation and design system** (Feature 05): design tokens,
  self-hosted fonts, UI primitives and the responsive app shell (`d80012a`).
- **PWA shell and offline** (Feature 06, `81c5085`).
- **Deployment, identity and backups** (Feature 07, `67e44a5`).
- **LLM provider layer** (Feature 08): adapters for OpenAI-compatible,
  Anthropic and Ollama endpoints over plain HTTP, with provider presets,
  retry and backoff (`8adbd2d`).
- **AI harness and skills framework** (Feature 09): structured output with
  one repair retry, and an `ai_runs` record of every run (`72fa311`).
- **Recipe grounding and photos** (Feature 10): recipes found through
  SearXNG or Brave search and read from the page's schema.org data, with
  the photos cached locally (`f25b28b`).
- **Settings home and Admin AI assistant** (Feature 11, `fcbdebd`).

### Household features (Features 12–24 and UI Bugs)

- **Regulars and onboarding** (Feature 12, `2b0e5ff`; release corrections
  `cb92071`, `4a20962`, `ac7453f`): three-step first-run onboarding that
  needs at least three Regulars; Regular dinners in Settings, added by name
  or by a pasted recipe link.
- **Discover — Side quests** (Feature 13, `5291457`): AI-suggested Side
  quests on a swipe deck. Try it puts one into the Quest log; Pass declines
  it, and the declined name is kept so later batches skip it. Each
  suggestion carries its reason ("Why this?"), which cites the household's
  Regulars.
- **Weekly plan — Draft board** (Feature 14, `1862f45`; corrections
  `5ac5982`): the AI drafts the week (Monday to Sunday) for the household
  to adjust by swapping dinners, with Rebuild week to redraft it. After a
  Quest-log dinner's planned date passes, the Rotation check asks "Keep it
  in rotation?". It never promotes a dinner to Regular on its own (D-021).
- **Google Calendar sync** (Feature 16, `7003ff9`): Push to Calendar
  mirrors the plan one way into the chosen Google Calendar, one event per
  day with deterministic event ids, so pushing again never adds a second
  event.
- **Supply run and Supply archive** (Feature 17, `cb451ba`; deflake
  `839eb61`): the week's shopping list grouped by dinner, made from the
  plan's recipes with no model call. When the week ends it is frozen into
  the blog-style Supply archive.
- **Cook from what I have and Quick dinners** (Feature 18, `47a5b5c`;
  release-review fixes `14f7141`; follow-up `4dcfc3c` withholds unsafe
  recipe sources from idea results): ideas from the ingredients on hand, and
  Quick dinners (a crock pot, or 30 minutes or less from prep to table).
- **Recipe detail and Kitchen reading mode** (Feature 15, `4b592fd`):
  `/dinner/:id` shows a dinner's recipe, in Kitchen reading mode on a
  tablet.
- **Notifications and Google Chat** (Feature 19, `dcd35e7`): Web Push and
  Google Chat for `plan_ready`, `list_ready`, `day_of_reminder`,
  `new_suggestions` and `rotation_review`, chosen per event.
- **The Scheduler's weekly rhythm** (Feature 20, `ecf7c6b`), on household
  times you can change. The defaults are: new Side quests on Sunday
  at 08:00, the plan drafted on Friday at 09:00, and the Supply run sent to
  Google Chat on Saturday at 08:00. Every day it sends a dinner reminder at
  16:30 (plus a crock-pot start reminder when needed) and runs the Rotation
  check at 09:00. On Monday at 00:05 it moves last week's Supply run into
  the Supply archive (fixed time).
- **Home dashboard** (Feature 21, `fd087e0`): `/` shows tonight's dinner,
  the Side quest count, the Supply run's state and recent updates.
- **Meal photos for manually added dinners** (Feature 23, `34e8496`): a
  photo backfill job grounds each dinner that failed grounding and has no
  photo, started from Regular dinners in Settings.
- **Automated deploys** (Feature 24, `884d166`): a systemd timer deploys
  the new `main` without anyone at the host, following the runbook's
  steps. Each run stops at the first failure and never retries a SHA that
  failed.
- **Docs refresh** (`13ddf4f`): README, AGENTS.md and ARCHITECTURE.md
  describe what was built.
- **UI Bugs fixes** (`9a7aa12`, feature pull request #4):
  - The mobile bottom bar now paints above the scrolled page.
  - A dinner's recipe opens from the tablet and desktop plan manager's
    day cards, from Regular dinners rows and from Supply run dinner names.
  - Recipe affordances are buttons that check with the server before
    opening, so a dinner archived on another device shows as removed
    rather than as a dead link.
  - Reminders name their dinner.

### Launch readiness work — not in this release

When this release was cut (`dev` at `9a7aa12`, promoted to `main` as
`273cf8f`), the Launch readiness feature branch
(`feature/7383c2cb-3179-4b45-b558-bf1bc1833fad-launch-readiness`, then at
`48c4c0e`) had not been merged into `dev`; it was 21 commits ahead of it.
Its work reaches `dev` through feature pull request #13 and reaches `main`
only with a later promotion. That work includes:

- the empty, loading and error state sweep (#187)
- portrait tablet layouts (#188)
- the accessibility pass (#189)
- login state made single-use (#193)
- Supply run and Supply archive fixes (#199–#201)
- the Lighthouse launch summary (#204)
- the production smoke evidence (#212)
- the favicon (#213)

None of it is in v1.0.0 as described here (`273cf8f`), whatever the state of
`dev` when you read this.

## Known limitations

- **The mobile Draft board still swaps on tap.** Its tile keeps
  swap-on-tap; only the tablet and desktop plan manager opens a dinner's
  recipe from its day card (`9a7aa12`). Whether the mobile tile should open
  the recipe instead is recorded as an owner question.
- **Push to Calendar event descriptions link to `/dinner/{id}`.** Each
  event's description ends with `{MEALQUEST_PUBLIC_URL}/dinner/{id}`
  ([ARCHITECTURE.md](ARCHITECTURE.md) "Google Calendar"). The link lives in
  Google Calendar, outside the app, so the server check that guards
  in-app recipe buttons does not apply to it.
- **A refused Regular removal stays hidden on that device** until the
  server settles it, with a 2xx or a 404 to the retried `DELETE`. This is
  by design ([ARCHITECTURE.md](ARCHITECTURE.md) "Frontend").
- **Deferred LOW items:**
  - the plan-card test uses a placeholder route;
  - an offline saved Supply run banner can go stale for a week evicted by
    a third save.
- **Layout checks ran in jsdom only.** The bottom bar and long-list checks
  in the UI Bugs tests ran in jsdom, which does no layout. Pixel-level
  confirmation on a real browser or phone is not recorded (`9a7aa12`
  commit message).
- **The GitHub deploy gate is not recorded as run in production.** These
  notes do not record a deploy of a `main` commit gated by GitHub CI
  (see "After the v1.0.0 cut — in this tree").

### What has actually been verified

These are owner confirmations of 2026-10-25:

- The deployed service answers `GET /health` with
  `{"status":"ok","version":"561f249"}`.
- Google Calendar sync works against the real account.
- Push delivery is confirmed on two devices.

Nothing else here is claimed as verified on a real device or platform.

## Setup

Configuration comes only from the environment (`MEALQUEST_*`). If a
required variable is missing or invalid, the service refuses to start and
names the variable (`internal/config`). The full procedures are in the
CTXD project `mealquest`, file `RUNBOOKS.MD`, and in
[deploy/RUNBOOK.md](../deploy/RUNBOOK.md). They are not repeated here.

### What a reader must provide

- **Environment** (`deploy/.env`, template
  [deploy/.env.example](../deploy/.env.example), never committed):
  - `MEALQUEST_PUBLIC_URL` — must be `https://`, except on localhost.
  - `MEALQUEST_SHUTDOWN_TIMEOUT`
  - `MEALQUEST_DEFAULT_TZ` — needed until the household settings exist on
    first boot, and ignored after that.
  - `MEALQUEST_OIDC_ISSUER`, `MEALQUEST_OIDC_CLIENT_ID` and
    `MEALQUEST_OIDC_ALLOWED_GROUPS`
  - `MEALQUEST_GOOGLE_CLIENT_ID`
  - `MEALQUEST_VAPID_PUBLIC_KEY` and `MEALQUEST_VAPID_SUBJECT`
  - optional: `MEALQUEST_TRUSTED_PROXY`, `MEALQUEST_SESSION_IDLE` and
    `MEALQUEST_SESSION_MAX`
  - `MEALQUEST_BIND_ADDRESS`, for compose.

  [deploy/compose.yaml](../deploy/compose.yaml) fixes
  `MEALQUEST_HTTP_ADDR`, `MEALQUEST_DATA_DIR`, `MEALQUEST_SEARXNG_URL` and
  the `*_FILE` paths.
- **Secrets, read only from files named by `*_FILE` variables.** The
  service refuses a secret set inline.
  - `MEALQUEST_SECRET_KEY_FILE`
  - `MEALQUEST_OIDC_CLIENT_SECRET_FILE`
  - `MEALQUEST_GOOGLE_CLIENT_SECRET_FILE`
  - `MEALQUEST_VAPID_PRIVATE_KEY_FILE`

  In production these are the files `secret_key`, `oidc_client_secret`,
  `google_client_secret` and `vapid_private_key` in `deploy/secrets/`, with
  `litestream_key` and `searxng.env` beside them. None is committed.
- **Authentik:** an OIDC application, with its users in the allowed group
  (`mealquest-admin` in production).
- **Google OAuth client:** a web client whose authorized redirect URI is
  `<MEALQUEST_PUBLIC_URL origin>/api/v1/integrations/google/callback`.
- **VAPID:** a key pair printed by `go run ./cmd/vapidgen`. The private
  key must belong to the public key.
- **SearXNG sidecar:** the `searxng` service in the compose stack, on the
  internal network only, with its secret in `deploy/secrets/searxng.env`.
  Brave search is the alternative provider in the recipe search settings.
- **Litestream backups:** the `litestream` service replicates the database
  to the NAS over SFTP ([deploy/litestream.yml](../deploy/litestream.yml)).
  Cached photos are backed up nightly by `mealquest-media-backup.timer`.

### Where the runbooks are

- CTXD `mealquest` `RUNBOOKS.MD` sections:
  - "Local development"
  - "Stack and first deploy" (steps 3 "Environment file" and 4 "Secrets")
  - "Authentik"
  - "Google Calendar OAuth client"
  - "Google Chat webhook"
  - "Stack unit"
  - "Recipe search (SearXNG)"
  - "Routine deploy and rollback"
  - "Backups" ("Database — Litestream", "Media — nightly rsync")
  - "Restore the database"
  - "Restore photos (media)"
  - "Secret rotation", including "VAPID keys (Web Push)"
- [deploy/RUNBOOK.md](../deploy/RUNBOOK.md) covers:
  - build, ship and restart
  - verifying the deployed version and a real screen
  - rollback to a previous release
  - "Automated deploys", including installing and enabling the timer
- Releases: only `overseer` merges `dev` into `main` ([AGENTS.md](../AGENTS.md)).

## Sources

These notes are drawn from:

- `git log` on `dev` and `origin/main`, the launch readiness branch, and
  pull request #10
- [README.md](../README.md), [AGENTS.md](../AGENTS.md),
  [docs/ARCHITECTURE.md](ARCHITECTURE.md),
  [deploy/RUNBOOK.md](../deploy/RUNBOOK.md),
  [deploy/compose.yaml](../deploy/compose.yaml),
  [deploy/.env.example](../deploy/.env.example)
- `internal/config/config.go`, and on this tree `deploy/auto-deploy.sh`
  and `.github/workflows/ci.yml`
- the section headings of the CTXD `RUNBOOKS.MD`
- Maestro task 6fc68c88, for the owner confirmations and the deferred
  items
