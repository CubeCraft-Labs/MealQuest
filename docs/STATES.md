# Empty, loading and error states

Every route the PWA serves — the handoff's routes plus the added Settings
screens — with its empty, loading and error state. The handoff does not mock
these states; it gives one pattern for each
([handoff README](../design/MealQuest%20UI%20Designs/design_handoff_mealquest/README.md),
"Empty / loading / error states"), and every screen uses the shared component
for it:

| State | Component | Pattern |
| --- | --- | --- |
| Loading | `Skeleton` ([web/src/ui/Skeleton.tsx](../web/src/ui/Skeleton.tsx)) | Pill-shaped skeletons in `--color-surface` with a 1.2 s opacity pulse (no pulse under reduced motion). |
| Error | `ErrorBanner` ([web/src/ui/ErrorBanner.tsx](../web/src/ui/ErrorBanner.tsx)) | `--color-neutral-100` card, grape-700 (`--color-accent-700`) text, and a **Retry** button that reads or asks again. |
| Empty | `EmptyState` ([web/src/ui/EmptyState.tsx](../web/src/ui/EmptyState.tsx)) | Bagel title, body text and an optional primary action — the Discover deck's "All caught up / New side quests arrive Sunday." with "Get more now" is the model. |

[web/src/states.test.tsx](../web/src/states.test.tsx) renders the loading,
empty and error variant of each route below against a stubbed API, asserting
the copy and pressing Retry. Each screen's own test file covers its other
states.

The loading and error rows name the screen's main read. Smaller reads on a
screen (a Quest log count, a calendar list, a webhook card) use the same
`Skeleton` and `ErrorBanner` in place.

## Handoff routes

| Route | Screen | Empty | Loading | Error |
| --- | --- | --- | --- | --- |
| `/` | Home | `EmptyState` "No quest planned tonight" with **Plan this week**; rows that do not apply are hidden. | Skeletons for the date, Tonight card and status rows. | "Couldn't load tonight's quest. …" + Retry. |
| `/onboarding` | Onboarding — Welcome step | None: the step reads nothing; it shows the welcome and the Regulars, Side quests and Quest log terms with **Let's start**. | None: nothing to read. | None: nothing to read. |
| `/onboarding/regulars` | Onboarding — Regulars step | "Your regulars · 0" with the Common picks to add; **Next: notifications** stays disabled until enough Regulars are saved. | Skeleton under "Your regulars". | "Your Regulars could not load: …" + Retry. |
| `/onboarding/notifications` | Onboarding — Notifications step | None: the step reads nothing. | While finishing, **Turn on notifications** and **Maybe later** are disabled. | "Could not finish setting up: …"; the buttons come back to try again, with **Add Regulars** when fewer than three Regulars are saved. |
| `/plan` | Weekly plan (Draft board) | `EmptyState` "Nothing planned yet" with **Draft this week**. | Seven skeleton tiles ("Plan loading"; "Drafting this week" while the AI drafts). | "Couldn't load this week's plan. …" + Retry. |
| `/plan/shopping` | Supply run | `EmptyState` "No shopping list yet" with **Make the shopping list**; an archived week with none shows "No shopping list". A list with no ingredients shows "Nothing to buy". | Skeleton title and dinner cards. | "Couldn't load the shopping list. …" + Retry. |
| `/plan/shopping/archive` | Supply archive | `EmptyState` "Nothing archived yet" — "Each week's Supply run becomes a post here once it's made." | Skeleton posts. | "Couldn't load the Supply archive. …" + Retry. |
| `/discover` | Side quests deck | `EmptyState` **"All caught up"** — "New side quests arrive Sunday." with **Get more now**. | Skeleton card stack (also while a batch finds more). | "Couldn't load your side quests. …" + Retry. |
| `/quick` | Quick dinners | No ideas found: "Couldn't find Quick dinners. No ideas came back with a recipe to match." + Retry (an `ErrorBanner`, as no idea was found to show). | Six skeleton rows while the tab's ideas are found. | "Couldn't start looking for Quick dinners. …" + Retry. |
| `/cook-from-pantry` | Cook from what I have | Before a search: no ingredient chips and **Find ideas** disabled. No ideas found: "Couldn't find ideas. No ideas came back with a recipe to match." + Retry. | Five skeleton cards ("Ideas loading"). | "Couldn't start looking for ideas. …" + Retry. |
| `/dinner/:id` | Dinner detail | `EmptyState` "Dinner not found" — "This dinner isn't in our kitchen — it may have been removed." | Skeleton hero, title and lines. | "Couldn't load this dinner. …" + Retry. |
| `/settings` | Settings home | Status lines "None on rotation yet", "Not connected", "Push off · Google Chat not connected". | A skeleton per status line. | "Settings could not load: …" + Retry, which reads every failed status again. |
| `/settings/notifications` | Notifications & Google Chat | No webhook stored: the webhook form with **Save webhook**. | Skeletons for the channel matrix and the webhook card. | "Notification settings could not load: …" + Retry ("The Google Chat webhook could not load: …" for the webhook card). A failed webhook test says "Test message not sent: …" under the form. |
| `/settings/ai` | Admin · AI assistant | No key stored: "No key stored" with **Add key**. | Skeleton. | "AI settings could not load: …" + Retry. |

## Added Settings screens

| Route | Screen | Empty | Loading | Error |
| --- | --- | --- | --- | --- |
| `/settings/household` | Household | None: the household's settings always exist, seeded on first start; the screen shows the seeded values. | Skeleton. | "Household settings could not load: …" + Retry. |
| `/settings/regulars` | Regular dinners | "On rotation · 0" — "No Regulars yet. Add the dinners your household makes on repeat." | Skeleton. | "Your Regulars could not load: …" + Retry. |
| `/settings/calendar` | Google Calendar | "Not connected" with **Connect Google Calendar**. | Skeleton (and one for the calendar picker). | "Google Calendar status could not load: …" + Retry ("Your calendars could not load: …" + Retry for the picker). |
