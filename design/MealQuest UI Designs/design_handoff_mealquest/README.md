# Handoff: MealQuest — mobile-first meal planning PWA

## Overview
MealQuest helps one household plan weekly dinners. The household enters the dinners they make on repeat ("regulars"); an AI assistant suggests new dinners to try ("side quests"), which the household accepts or declines with a swipe-card UI. Accepted dinners go into a to-try pool (the "Quest log"). Each week the AI drafts a plan that mixes regulars with Quest-log picks; the household swaps days, then the plan is pushed to Google Calendar. A shopping list is generated from the planned dinners, grouped by dinner, minus anything the household unchecks as already on hand. Each week's list is archived as a "post" in a blog-style history (by week / month / year). Push notifications and Google Chat webhook messages announce key events. An admin area inside Settings configures the LLM.

### Product decisions (confirmed with the client)
| Topic | Decision |
|---|---|
| Users | One **shared household account** (no per-member profiles). Anyone in the household can edit everything, including Admin. |
| Swipe accept | Goes into the **Quest log** (to-try pool), scheduled later. |
| Suggestion card shows | Photo, **why it was suggested** (referencing regular dinners), **difficulty**. |
| Week building | **AI builds the week** mixing regulars + Quest-log meals; the user swaps/adjusts. No fixed new-vs-regular ratio. |
| Google Calendar | **One-way: app is source of truth**, pushes dinners to a Google Calendar. |
| Shopping list | All ingredients for planned dinners, **grouped by dinner**. The user **unchecks items they already have** (this is the only pantry mechanism — no persistent pantry). |
| History | **Each weekly shopping list is a new post**; filter Week / Month / Year. |
| Quick dinners | "Quick" = **crock pot** OR **≤ 30 min prep-to-table**. Shown as **two tabs** (Crock pot / 30 min or less). Reachable from a **bottom-nav tab** AND a Home shortcut, and as a "Quick only" filter in Cook-from-what-I-have. |
| Notifications (push + Google Chat) | New weekly plan ready · Shopping list ready · Day-of dinner reminder · New suggestions to swipe. Each event is toggled separately for Push and Chat. |
| Admin (LLM) | Provider + model picker · API key management · Temperature (creativity) · System prompt editor. Lives **inside Settings**. |
| Copy tone | Playful, quest-themed, but every label stays plain enough to understand. |

### Vocabulary (use consistently in UI copy)
- **Regulars** — the household's routine dinners.
- **Side quests** — AI dinner suggestions (Discover tab).
- **Quest log** — accepted suggestions waiting to be scheduled (to-try pool).
- **Supply run** — the current week's shopping list.
- **Supply archive** — shopping-list history.

## About the design files
The files in this bundle are **design references built in HTML** — static hi-fi mockups plus one interactive motion prototype. They show the intended look and behavior; they are **not production code to copy**. Recreate them in the target codebase using its established patterns. There is no existing codebase, so the recommended stack is:

- **React 18 + TypeScript + Vite**, `vite-plugin-pwa` (Workbox) for the service worker + manifest.
- Styling: CSS variables from `tokens.css` + CSS Modules or Tailwind configured to those variables. Do not hard-code hex values in components.
- Motion: **Web Animations API** (as in the prototype) or **Framer Motion** — the keyframes below translate 1:1.
- Icons: **Lucide** (`lucide-react`), `strokeWidth={2.75}` everywhere.
- Routing: React Router. Data: TanStack Query against a small API (backend out of scope for this handoff; integration points listed under *State & data*).

## Fidelity
**High-fidelity.** Colors, type, radii, spacing, copy and layout are final. Recreate pixel-accurately. Photos are placeholders (striped boxes labelled "dish photo") — real recipe photography will be supplied and should be rendered through the Organic "washed" treatment (`filter: saturate(.6) contrast(.85) brightness(1.1) opacity(.94)`), with rounded corners.

---

## Design tokens
All values are in `tokens.css`. Summary:

**Color**
| Role | Token | Hex |
|---|---|---|
| Page / phone background | `--color-bg` | `#f9f6f1` |
| Surface (cards, rows, inputs) | `--color-surface` | `#eee9de` |
| Ink | `--color-text` | `#201e1d` |
| Primary accent (Grape) | `--color-accent` | `#5a358c` |
| Text on accent | `--on-accent` | `#f9f6f1` |
| Second accent (Teal blue) | `--color-accent-2` | `#29889c` |
| Divider | `--color-divider` | `#201e1d` @ 16% |

Ramps (100→900): Grape `#f8f1ff #eedfff #dac3ff #c1a1f9 #a47ee1 #865fc0 #68449b #4a2d73 #2d1a49`; Teal `#e0fbff #bbf3ff #88e0f6 #3fc7e4 #00a9ca #008aaa #006b88 #004c63 #002f3e`; Neutral `#f9f4ed #eee7db #dcd3c4 #c0b6a5 #a19786 #82796a #645c50 #474238 #2e2b25`.

Usage rules:
- Grape fill (`--color-accent`) = primary buttons, active nav pill, selected segmented option, checked checkboxes, slider fill. **Always `--on-accent` text on it.**
- Grape small text (kickers, links) uses `--color-accent-700`.
- "Side quest"/new-dinner highlights: grape-100 fill + grape-300 inset ring (rows) or teal-200/300 fills (tiles). Tags: grape-100 bg / grape-800 text.
- Teal = positive/confirmation voice: "Why this?" boxes (teal-100 bg, teal-900 text), "Synced"/"Connected" status text (teal-800), toggles ON and "Try it" button (teal-600), Quest-log count pill (teal-200 bg, teal-800 text).
- Neutral-100 = secondary card fill; neutral-700 = muted meta text.

**Typography**
| Use | Font | Size / line-height |
|---|---|---|
| Mobile screen title | Bagel Fat One 400 | 26–30px / 1.1 |
| Card title (suggestion) | Bagel Fat One | 24px / 1.1 |
| Section/card heading | Bagel Fat One | 16–22px / 1.15 |
| Tablet/desktop page title | Bagel Fat One | 36–40px / 1.1 |
| Buttons | Bagel Fat One | 14–16px |
| Body | Figtree 400 | 13–15px / 1.45–1.55 |
| Row label | Figtree 700 | 14–15px |
| Kicker | Figtree 700, uppercase, letter-spacing .1em | 11–12px |
| Meta / tag | Figtree 700 | 10–12px |
| Mono (URLs, keys, system prompt, photo placeholder) | ui-monospace | 11–13px |

**Shape:** buttons, inputs, tags, nav, segmented controls are pills (999px). Mobile cards 24–32px radius; list rows 18–24px; inner photo 24px inset 10px from card edge. Phone frame in mocks is 36px (device chrome only).

**Elevation:** `--shadow-sm` (segmented selected), `--shadow-md` (round action buttons, tiles), `--shadow-lg` (top swipe card, dragged items).

**Icons (Lucide, stroke 2.75):** house, calendar, compass, zap, sliders-horizontal, x, heart, check, chevron-left/right/down, refresh-cw (swap), plus, shopping-cart, message-square, bell, search, archive, users. Nav icons 21–22px; inline 15–18px.

---

## Layout & navigation

### Breakpoints
- **Mobile < 768px** — design width 360–390px. Bottom nav bar.
- **Tablet 768–1199px** — design 1180×820 landscape. 96px left icon rail.
- **Desktop ≥ 1200px** — design 1440×900. 208px left sidebar.

### Primary nav (5 items, same order everywhere)
`Home · Plan · Discover · Quick · Settings`
- **Mobile bottom bar:** absolute, `left/right 12px`, `bottom 16px` (+ safe-area inset), height 62px, pill, background `--color-text`, `justify-content: space-around`, padding 0 6px. Inactive items: icon only, color `#c0b6a5` (neutral-400). Active item: icon + label (Figtree 700 13px) on a grape pill (height 42px, padding 0 12–14px, `--on-accent` text). **The grape pill is one shared element that slides between items — see Motion.**
- **Tablet rail:** 96px, `--color-surface` bg. Top: 48px grape circle with "M". Items stacked, gap 18px: 56×36 icon pill (active = grape fill) over an 11px 700 label.
- **Desktop sidebar:** 208px, `--color-surface`, padding 28px 16px. "MealQuest" wordmark (Bagel Fat One 28px, grape). Items: pill rows 12px 16px, gap 12px, Figtree 700 15px; active = grape fill + on-accent text. Footer card "Our kitchen / Shared household" with 36px teal avatar.

### Routes
| Route | Screen | Mock id |
|---|---|---|
| `/` | Home | 1g |
| `/onboarding` | Add regular dinners (step 2 of 3 shown) | 1h |
| `/plan` | Weekly plan (draft board) | **1f** (mobile), 2c (tablet), 2d (desktop) |
| `/plan/shopping` | Shopping list | 1l, 2b, 2f |
| `/plan/shopping/archive` | Supply archive | 1m, 2f (right panel) |
| `/discover` | Swipe suggestions | **1a** |
| `/quick` | Quick dinners (tabs) | 1j |
| `/cook-from-pantry` | Cook from what I have | 1i |
| `/dinner/:id` | Recipe / dinner detail | 1k, 2a (tablet reading mode) |
| `/settings` | Settings home | 1p |
| `/settings/notifications` | Notifications & Google Chat | 1n |
| `/settings/ai` | Admin · AI assistant | 1o |

Desktop settings (2e) shows Notifications, Google Chat webhook and AI assistant on one page (two columns).

**Chosen mobile directions:** Discover = **1a** (card stack), Plan = **1f** (draft board). Mocks 1b, 1c, 1d, 1e are rejected alternatives — ignore them.

---

## Screens

All mobile screens: background `--color-bg`; status-bar area 40px; content side padding 20px; bottom nav overlays the last 94px, so scrollable content needs ≥ 100px bottom padding.

### 1g · Home
- Header: "Friday, Oct 2" (12px 700 neutral-700) over title "Tonight's quest". Right: 40px round bell button (surface) with 8px grape unread dot.
- Tonight card (surface, radius 32): 150px photo inset 10px, then dinner name (Bagel 22px), meta "Regular · 40 min", right-aligned dark pill button "Recipe" (`--color-text` bg, `--color-bg` text).
- Two 104px tiles, 2-col grid gap 10: **Quick dinner** (grape fill, zap icon, on-accent text) → `/quick`; **Cook from what I have** (teal-300 fill, teal-900 text, search icon) → `/cook-from-pantry`.
- Status rows (neutral-100, radius 20, 12px 14px): "10 new side quests to swipe" → Discover; "Shopping list ready · Oct 5–11" → Shopping. Icon in grape-700, chevron right.

### 1h · Onboarding — regular dinners
3 progress bars (6px pills, filled = grape). "Step 2 of 3" kicker, title "What's on your usual rotation?" (30px), helper "Add the dinners your household makes on repeat. MealQuest builds suggestions from these." Input pill (48px, surface) with 36px grape "+" button. "Your regulars · N": chips teal-200/teal-900 with "×". "Common picks": dashed-outline chips "+ Chili" etc. Fixed bottom primary button "Next: notifications" (52px).

### 1a · Discover — swipe (CHOSEN)
- Header: kicker "Discover", title "Side quests"; right pill "Quest log · 6" (teal-200/teal-800).
- Deck area: margin 18px 20px, height 440px, `perspective: 1000px`. Three stacked cards: depth 0 `none`, depth 1 `translateY(8px) scale(.95)`, depth 2 `translateY(16px) scale(.9)`. Top card bg `#f9f4ed`, shadow-lg; depth 1 surface; depth 2 neutral-300. Radius 32.
- Card content: photo 220px inset 10px (radius 24); title (Bagel 24px); difficulty = 3 dots 8px (filled grape, empty neutral-300) + label "Easy/Medium/Hard"; "Why this?" box (teal-100, radius 18, 12px 14px, 13px/1.45) — e.g. "You make Chicken stir-fry and Beef tacos most weeks. Same flavors, sweet-savory twist."
- Stamps (hidden until gesture): "Try it!" top-left, rotate −12°, teal; "Pass" top-right, rotate 12°, neutral.
- Actions (centered, gap 28, 22px below deck): 62px round **Pass** (neutral-100, X) and **Try it** (teal-600, heart, on-accent icon), 11px labels below.

### 1f · Plan — draft board (CHOSEN)
- Header: "Oct 5 – 11" (26px) + "Draft" tag (grape-100/800). After pushing, tag becomes "Synced".
- Assistant bubble: 32px grape "Q" avatar + bubble (surface, radius 4 18 18 18): "I mixed 5 of your regulars with 2 from your Quest log. Tap any day to swap it."
- 2-col grid, gap 8, tiles 86px, radius 22, padding 12: day kicker (10px 700 .1em) + dinner (Bagel 15px). Regular = surface; Quest-log pick = teal-300 with "WED · NEW" kicker (teal-900). Swapping state = bg + 2px grape inset ring, kicker "FRI · SWAPPING…", muted name. 8th tile = dashed "+ Rebuild week".
- Primary button "Looks good — push to Calendar" (50px).
- Tapping a tile opens a swap sheet (not mocked; use Quest log + Regulars lists from 2d's right panel as a bottom sheet).

### 1j · Quick dinners
Header with 36px grape zap circle + "Quick dinners". Segmented tabs (surface track, 4px padding; selected = bg + shadow-sm): **Crock pot | 30 min or less**. Helper line per tab ("Prep to table in 30 minutes or less, picked from your tastes." / crock-pot equivalent). Rows (neutral-100, radius 24): 64px round photo, name (Bagel 16px), "25 min · Medium", 36px teal "+" (adds to Quest log). Footer link "↻ Show me more" (grape-700). Nav: Quick active.

### 1i · Cook from what I have
Back button + title. Ingredient chip field (surface, radius 26; chips bg-colored "Chicken thighs ×"; trailing "Add ingredient…"). Segmented "Any time | Quick only" + dark "Find ideas" button. Result cards (neutral-100, radius 24): name + time, teal-800 line "Uses all 5 · missing: 1 lime", buttons "Cook tonight" (grape) and "Add to Quest log" (outline).

### 1k · Recipe detail (mobile) / 2a · Kitchen reading mode (tablet)
Mobile: 250px full-bleed photo with round back button; sheet overlaps by 28px (radius 32 top). Tags (Side quest / Easy / 30 min), title 28px, "Why it was picked" teal box, Ingredients list with qty right-aligned and dividers, sticky bottom "Add to this week" + heart button.
Tablet 2a: header (back, "Saturday, Oct 10 · Side quest", title 36px) with **Keep screen on** toggle (Screen Wake Lock API) and **text size** segmented A/A/A. Two columns `400px | 1fr`: Ingredients card with 22px checkboxes (checked = struck-through, "3 of 9 set out"); Steps list — completed step muted with check, **current step** highlighted (grape-100 bg, 2px grape-300 ring, number circle grape, text 22px), others 18px. "Previous / Next step" buttons bottom-right.

### 1l · Shopping list / 2b tablet / 2f desktop
Kicker "Supply run · Oct 5–11", title "Shopping list", count "19 / 23". Helper "Uncheck anything you already have — it won't be counted." One card per dinner (surface, radius 24): dinner name (Bagel 16px) + day (11px). Items: 20–22px checkbox (checked = grape fill w/ white check = **to buy**; unchecked = 2px neutral-400 outline, struck-through name, "have it" label). Status line: "Sent to Google Chat · Sat 8:00 am" (teal-800, message icon). Tablet/desktop: CSS `column-count: 3` masonry, actions "Archive" (outline) + "Send to Google Chat" (primary).

### 1m · Supply archive (and 2f right panel)
Segmented "Week | Month | Year". Month headings (Bagel 18px). Post cards: kicker "WEEK 41 · OCT 5 – 11", AI-written title e.g. "Tacos, tikka & a salmon finale", meta "7 dinners · 23 items · 4 already on hand", dinner chips. Current week post = grape-100 + grape-300 ring, "· Current". Tapping a post opens the frozen list (read-only).

### 1p · Settings home
Title "Settings". Household banner (teal-100, 44px avatar "K", "Our kitchen / Shared household · changes apply to everyone"). Grouped list cards (surface, radius 26; rows 14px 16px; 38px round icon; chevron):
- HOUSEHOLD: Regular dinners ("7 on rotation"), Google Calendar ("Connected · Dinners calendar").
- ALERTS: Notifications & Google Chat.
- ADMIN: AI assistant ("Anthropic · Claude Sonnet") + "Admin" tag; card has grape-300 inset ring.

### 1n · Notifications & Google Chat
Back + title. Grid `1fr 46px 46px`: Event | PUSH | CHAT with toggles (40×24 pill; ON teal-600, knob right; OFF neutral-300). Events: New weekly plan ready · Shopping list ready · Day-of dinner reminder ("at 4:30 pm", tappable time) · New suggestions to swipe. Google Chat webhook card: mono URL input (ellipsis), "Connected · Dinner Crew space", "Send test" button. Message preview bubble: "MealQuest · preview — This week's plan is ready: Tacos, Bolognese, Tikka masala + 4 more. Open the plan →".

### 1o · Admin · AI assistant
Back + kicker "Settings · Admin" + title "AI assistant". Provider + Model dropdown pills (2-col). API key: masked mono "sk-••••••••3f9a" + "Replace" chip; "Verified · Oct 1" (teal-800). Temperature slider 0–1 step 0.1 (8px track, grape fill, 24px knob with 3px grape ring), labels "Familiar ↔ Adventurous", value right. System prompt textarea (mono 12px, radius 22). Bottom: "Reset" (outline) + "Save changes" (primary). Store API keys server-side only; the client only ever sees the masked tail.

### 2c tablet / 2d desktop · Plan manager
Header: kicker "Week 41", title "Oct 5 – 11", prev/next circles (+ "This week" pill on desktop), actions "Shopping list · 28", "Rebuild week", "Push to Calendar" (primary). Status "Synced to Google Calendar 2 min ago" (teal-800). 7-column grid (`repeat(7, minmax(0,1fr))`, gap 12): day label + card (photo 96–110px, name Bagel 16–17px, time, crock-pot "Start by 12:30 pm" hint, tag Regular/Side quest, swap icon). Side-quest cards teal-200.
Desktop right panel (290px, left divider): segmented "Quest log · 6 | Regulars · 9", helper "Drag a dinner onto a day to swap it in.", draggable rows (48px photo, name, difficulty, 6-dot grip). **Drag state:** source row becomes a dashed ghost at 55% opacity; target day shows dashed 2.5px grape border on grape-100 with "Drop to swap / Replaces Pizza night"; dragged card floats with shadow-lg, rotate −3°. Tablet 2c shows the Quest log as a chip strip under the grid with the same drag behavior.

### 2e · Desktop settings
Title + "Changes apply to everyone in the household." Sub-tabs: Household · Regular dinners · **Notifications & AI**. Left column: Notifications matrix card, Google Chat webhook card. Right column: AI assistant card (same fields as 1o, prompt textarea flexes to fill).

---

## Motion
A working reference is in **`MealQuest Motion.dc.html`** (open in a browser; tap buttons, drag the card, switch tabs, toggle reduced motion). Implement with WAAPI or Framer Motion using these exact values.

### 1. Bottom-nav pill slide
One absolutely positioned grape pill (`top:10px; height:42px`) lives behind the nav items (items are `position:relative; z-index:1`).
1. On tap, record each item's `getBoundingClientRect().left` and the pill's current `left/width`.
2. Commit the new active tab (active item now renders its label, so items reflow).
3. Measure the active item's `offsetLeft/offsetWidth`; set the pill to it and animate **from old → new `left` + `width`, 420ms, `cubic-bezier(.34,1.4,.64,1)`** (slight overshoot).
4. FLIP every nav item: animate `translateX(oldLeft − newLeft) → 0` with the same duration/easing so icons glide instead of jumping.
5. Label fades in: `opacity 0, translateX(-4px) → 1, none`, 220ms ease-out, **120ms delay**.
6. Icon color crossfades (inactive `#c0b6a5` → `--on-accent`) via `transition: color .2s`.
Recompute pill position on resize and after fonts load.

### 2. "Try it" (accept)
Triggered by the Try it button (gesture variant below).
1. Button bump: `scale 1 → .88 → 1.06 → 1`, 300ms ease-out.
2. "Try it!" stamp pops: `opacity 0, rotate(-12deg) scale(1.5) → opacity 1, rotate(-12deg) scale(1)`, 220ms spring, fill forwards.
3. **Wiggle:** card `rotate 0 → -4° → 4° → -3° → 2° → 0`, 420ms ease-in-out (transform-origin center).
4. **Fly off right:** keyframes `translate(0,0) rotate(0)` → at 18% `translate(-14px,4px) rotate(-3deg)` (anticipation) → `translate(150%,-60px) rotate(28deg); opacity 0`. 480ms, `cubic-bezier(.55,0,.8,.4)`, fill forwards.
5. On finish: add dinner to Quest log (optimistic), bump the "Quest log · N" pill (same bump as step 1), advance the deck (see 5).

### 3. "Pass" (decline)
1. Button bump (as above).
2. Set `transform-origin: 0% 50%` (left edge — the card folds over like a page).
3. Card keyframes (720ms total, fill forwards):
   - 0%: `rotateY(0deg)` — easing to next frame `cubic-bezier(.45,0,.85,.55)`
   - 45%: `rotateY(-80deg)` — easing to next frame `cubic-bezier(.4,0,.9,.6)`
   - 100%: `translate(-45%,560px) rotate(-35deg) rotateY(-90deg); opacity 0` (drops and tumbles off left)
4. Shade overlay inside the card (`#201e1d`, inset 0, radius 32): opacity `0 → .4 (45%) → .55`, 720ms — sells the fold.
5. Parent deck container has `perspective: 1000px`.
6. On finish: record the decline (so the AI doesn't re-suggest), advance the deck.

### 4. Swipe gesture (top card only)
- Pointer events with `setPointerCapture`; card has `touch-action: none`.
- While dragging: `transform: translate(dx, dy*0.3) rotate(dx/18 deg)`. "Try it!" stamp opacity = `clamp(dx/100, 0, 1)`; "Pass" stamp opacity = `clamp(-dx/100, 0, 1)`.
- Release with **dx > 110px** → accept: skip the wiggle, fly from the current transform to `translate(150%,-60px) rotate(28deg); opacity 0`, 340ms, same throw easing.
- Release with **dx < −110px** → pass: run the Pass fold, prefixing the first two keyframes with the current drag transform.
- Otherwise snap back to `translate(0,0) rotate(0)`, 420ms `cubic-bezier(.34,1.4,.64,1)`; reset stamps.
- Lock input (`busy`) while any exit animation runs.

### 5. Deck advance
After an exit, cancel the exited card's animations, remove it, and promote the others: depth 1→0 and 2→1 animate from their previous depth transform to the new one, **300ms `cubic-bezier(.2,.8,.2,1)`**. Prefetch suggestions so the deck always has ≥ 3 cards.

### 6. Reduced motion
Respect `prefers-reduced-motion: reduce`: nav pill jumps instantly (no FLIP/overshoot); Try it / Pass become a 200ms opacity fade; no wiggle, fold or deck promotion.

### Other transitions (not prototyped; keep consistent)
- Screen push/pop: 260ms `cubic-bezier(.2,.8,.2,1)`, slide 24px + fade.
- Toggles: knob slides 180ms spring; track color 150ms.
- Checkbox check: scale `.8 → 1` 160ms spring; strike-through fades in 150ms.
- Bottom sheets: rise from 100% → 0, 320ms `cubic-bezier(.2,.8,.2,1)`, scrim fade to 50% neutral-900.
- Buttons: hover = one ramp step darker (`--color-accent-600`… on grape use `#4a2d73` for pressed), pressed `scale(.97)` 100ms. Focus: `outline 2px solid var(--color-accent); outline-offset 2px`.

---

## State & data

### Core entities
```ts
type Difficulty = 'easy' | 'medium' | 'hard';
interface Dinner { id: string; name: string; photoUrl?: string; minutes: number; method: 'stovetop'|'oven'|'crockpot'|'other';
  difficulty: Difficulty; ingredients: Ingredient[]; steps: string[]; source: 'regular'|'suggestion'; }
interface Ingredient { name: string; qty?: string; }
interface Suggestion { dinner: Dinner; reason: string; /* "why this?" copy from the LLM, cites regulars */
  status: 'pending'|'accepted'|'declined'; }
interface WeekPlan { weekStart: string /* ISO Monday */; days: { date: string; dinnerId: string; isNew: boolean }[];
  status: 'draft'|'synced'; calendarSyncedAt?: string; }
interface ShoppingList { weekStart: string; groups: { dinnerId: string; day: string;
  items: { name: string; qty?: string; needed: boolean /* false = "have it" */ }[] }[];
  sentToChatAt?: string; title: string /* blog post title, AI-generated */; }
interface Settings { notifications: Record<'planReady'|'listReady'|'dayOfReminder'|'newSuggestions', { push: boolean; chat: boolean }>;
  reminderTime: string; chatWebhookUrl?: string; calendarId?: string;
  ai: { provider: string; model: string; temperature: number; systemPrompt: string; apiKeyLast4?: string; keyVerifiedAt?: string }; }
```
`isQuick(d) = d.method === 'crockpot' || d.minutes <= 30` (prep-to-table).

### Client state
- `activeTab` (nav), deck `{ queue: Suggestion[], busy: boolean, drag?: {x,y,dx} }`, `questLogCount`.
- Plan: `selectedDay` for swap sheet, drag-and-drop source/target on tablet/desktop.
- Shopping list item toggles are optimistic and persisted per week; once the week ends, the list is frozen into the archive.

### Integration points (backend — out of scope, but the UI depends on them)
- **LLM** — suggestion generation, week drafting, ingredient-based ideas, quick ideas, archive post titles. Uses Admin settings. Never expose the API key to the client.
- **Google Calendar** — OAuth once (Settings → Google Calendar); "Push to Calendar" creates/updates one event per dinner (one-way).
- **Google Chat** — server POSTs to the stored incoming-webhook URL for enabled events; "Send test" posts a sample.
- **Web Push** — VAPID; request permission during onboarding step 3 ("notifications"), not on first load. iOS requires the PWA to be installed to the home screen — show an install hint.
- **PWA** — manifest (`name: MealQuest`, `theme_color: #5a358c`, `background_color: #f9f6f1`, `display: standalone`), offline cache for the current plan, shopping list and recipes (kitchen use).

### Empty / loading / error states (not mocked — follow the system)
- Deck empty: Bagel title "All caught up", body "New side quests arrive Sunday." + "Get more now" primary.
- Loading: pill-shaped skeletons in `--color-surface` with a slow 1.2s opacity pulse.
- Errors: inline teal/grape-toned banner (neutral-100 card, grape-700 text) with a retry button; webhook test failure shows the HTTP status under the URL field.

## Accessibility
- Swipe is never the only path: Pass / Try it buttons always present; also support ← / → keys on Discover.
- All text ≥ 4.5:1 (on-accent on grape ≈ 9:1). Hit targets ≥ 44px (nav items are 42px tall inside a 62px bar — keep the full bar height tappable).
- Toggles are `role="switch"`; checkboxes native inputs styled; nav items `aria-current="page"`.

## Assets
- Fonts: Bagel Fat One, Figtree (Google Fonts).
- Icons: Lucide.
- Photos: placeholders only — supply real dinner photos and apply the washed treatment.
- Base system: Organic design system (`_ds/.../styles.css`), overridden by `tokens.css`.

## Files
- `README.md` — this document.
- `tokens.css` — final design tokens (use these).
- `MealQuest Mockups v2.dc.html` — all screens. Section 2 = tablet & desktop (2a–2f); section 1 = mobile (1a–1p). Chosen mobile directions are badged "Chosen".
- `MealQuest Motion.dc.html` — interactive motion reference (nav pill, Try it, Pass, swipe).
- `screenshots/` — PNG of every chosen screen: `mobile-1a…1p-*.png` (720×1520, 2× of a 360×760 phone), `tablet-2a…2c-*.png` (1180×820), `desktop-2d…2f-*.png` (1440×900). Rejected mobile alternatives (1b–1e) are not included. Note: the static screenshot of 1a doesn't show the two stacked cards behind the top card; see Motion → deck depth transforms.
- `support.js`, `_ds/organic-…/styles.css` — needed only to open the HTML references locally (serve the folder over a local web server, e.g. `npx serve`).
