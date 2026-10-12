# Launch evidence

Evidence that the production PWA meets the launch gates. The raw reports and
the check script are committed under [lighthouse/](lighthouse/).

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
It loads each of the four pages above in a signed-in mobile Chrome session
(the cookie comes from the `CK` environment variable, never the file) and
checks:

- **Manifest** — the linked manifest is fetched (HTTP 200) and has
  `name` "MealQuest", `display` "standalone", a `start_url`, and icons at
  192 px, 512 px and a `maskable` purpose.
- **Service worker** — registered and activated, and controlling the page on
  the single-page-app routes.

The script's pass condition gates the manifest fields and icons and the
service-worker registration; the manifest HTTP status, the worker's activation
state and whether it controls the page are printed per page for review.

**Result: PASS on all four pages.**
