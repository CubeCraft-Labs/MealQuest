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
