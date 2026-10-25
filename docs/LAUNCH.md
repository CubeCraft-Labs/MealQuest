# Launch evidence

Evidence that the production PWA meets the launch gates: Lighthouse and
installability (2026-10-11), the end-to-end production smoke (2026-10-11/12)
and the final restore drill (2026-10-12). The raw Lighthouse reports and the
installability check script are committed under [lighthouse/](lighthouse/).

## Run conditions

| | |
| --- | --- |
| Target | Production, <https://mealquest.cubecraftlabs.com> |
| Build | `mealquest:743a155` |
| Session | Signed in (household session cookie, supplied at run time) |
| Date | 2026-10-11 |
| Tool | Lighthouse 13.5.0, mobile form factor |

The session cookie value is redacted from the committed reports
(`configSettings.extraHeaders`); nothing else in them is changed.

## Lighthouse

Reports: [lighthouse/2026-10-11/](lighthouse/2026-10-11/) — one
`.report.json` and one `.report.html` per page.

| Page | Route | Performance | Accessibility | Best practices |
| --- | --- | --- | --- | --- |
| Home | `/` | 71 | 100 | 100 |
| Plan | `/plan` | 72 | 100 | 100 |
| Discover | `/discover` | 66 | 100 | 100 |
| Dinner detail | `/dinner/<id>` | 70 | 100 | 100 |

**Gate: accessibility ≥ 95 on every page — met** (100 on all four).
Performance is recorded for reference but is not gated.

## Installability

Lighthouse 10 and later removed the PWA category, so installability was checked
directly with a Playwright probe:
[installability-check.mjs](lighthouse/2026-10-11/installability-check.mjs).
It loads each of the four pages above in a signed-in Chrome session with
Playwright's Pixel 5 mobile emulation (viewport, touch, `isMobile` and mobile
user agent; the cookie comes from the `CK` environment variable, never the
file) and gates every page on all of:

- **Manifest** — the linked manifest is fetched with HTTP 200 and has
  `name` "MealQuest", `display` "standalone", a `start_url`, and icons at
  192x192, 512x512 and one with `maskable` purpose.
- **Service worker** — `navigator.serviceWorker.ready` resolves within 10
  seconds (expiry is a failure), and a registration exists whose active worker
  is `activated`.

It prints a PASS or FAIL line per condition per page and exits 1 if any
condition fails on any page.

**Result: PASS on all four pages.**

## Production smoke — 2026-10-11/12

An end-to-end smoke of the launch flows against production, run with the
owner in the owner's signed-in session. Tracked as Maestro task
`0544e318-9b5f-4264-9884-1ec8cb63fd49`, closed 2026-10-12.

| | |
| --- | --- |
| Target | Production, <https://mealquest.cubecraftlabs.com> |
| Build | `mealquest:743a155`, then `mealquest:561f249` mid-smoke |
| Session | Owner session (signed in) |
| Google Chat | Test space "MealQuest - Test" (not the family space) |
| Google Calendar | Test calendar "MealQuest - Test" (not the family calendar) |
| Dates | 2026-10-11 to 2026-10-12 |

The smoke spanned two deploys: Feature 24 shipped mid-smoke through the new
auto-deploy path, moving production from `743a155` to `561f249`.

| Step | Result |
| --- | --- |
| Sign-in session and CSRF | ✓ |
| `/home` shows the Regulars | ✓ |
| Suggestion batch through the real LLM | ✓ 10 grounded cards, each "Why this?" citing the Regulars |
| Try it | ✓ dinner created with a grounded photo, added to the Quest log |
| Pass | ✓ declined dinner never re-suggested; deck 10 → 8 |
| AI draft, Week 42 (Oct 12–18) | ✓ 7 days, no duplicates, Quest-log pick marked `is_new`, `ai_note` cites the Regulars |
| Day swap | ✓ `PUT` of the day: Monday → Slow Cooker French Dip Sandwiches (crock pot) |
| Push to Calendar | ✓ status `synced`; owner confirmed 7 events; a second push made no duplicates |
| Supply run | ✓ 27 items, AI title "A Beefy Week with Pizza Night" |
| Have-it toggle | ✓ needed items 27 → 26 |
| Send to Google Chat | ✓ `sent_to_chat_at` stamped; owner confirmed receipt in the test space |
| Test push notification | ✓ HTTP 202, `event_key` `plan_ready:test-…`, 3 subscriptions; owner confirmed receipt on their iPhone |
| Quick dinner ideas | ✓ 6 ideas |
| Cook from what I have | ✓ 4 ideas, with uses and missing ingredients computed |

**Result: PASS on every step.**

Evidence form: server-side API responses and timestamps for the steps inside
the app, plus the owner's explicit confirmations for the three steps that
land outside it — the 7 calendar events, the Google Chat message and the
push notification on the phone. The owner's confirmation quotes are recorded
on Maestro task `0544e318-9b5f-4264-9884-1ec8cb63fd49`.

**Deviation: phone screenshots were not captured for any step.** The task
closed with the owner's explicit confirmations standing in for them. No
screenshot evidence exists for this smoke; whether the owner supplies
screenshots or confirms this disposition is pending the owner's decision.

## Final restore drill — 2026-10-12

Both halves of the restore procedure, run on the production stack with live
household data. Tracked as Maestro task `3b22e48b`, closed 2026-10-12.
The full record — commands, outputs and
checks — is in CTXD project `mealquest`, `RUNBOOKS.MD` (v12),
"Restore drill record — 2026-10-12"; the procedure itself is in the same
document.

| | |
| --- | --- |
| Target | Production stack, live household data |
| Deployed build | `mealquest:561f249` |
| Date | 2026-10-12 |

| Half | Checks | Result |
| --- | --- | --- |
| Database restore | Replica current; the restored file matched the before-snapshot exactly — dinners 26, week_plans 1, schema_migrations 18, content-sha256 `7d13261040238cd8`, integrity `ok`, zero loss; stack healthy at `561f249` after the restart | ✓ PASS |
| Media restore | All 102 NAS files byte-identical (`rsync --checksum`); 10 newer local photos preserved; an archived dinner's photo opened (`GET /media/photos/...-400.jpg` → 200 `image/jpeg`, 400×400); timer restarted | ✓ PASS |

**Result: PASS on both halves.**
