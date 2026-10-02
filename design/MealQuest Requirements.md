# MealQuest — Application Requirements

Everything decided while designing MealQuest, written so you can paste it into another LLM. Items marked **[Decided]** were confirmed by the client. Items marked **[Design default]** were proposed by the designer and not challenged; treat them as the current plan, but they can be changed.

---

## 1. Product summary
MealQuest is a **mobile-first Progressive Web App (PWA)** for weekly dinner planning for one household. The household enters the dinners they make regularly. An AI assistant suggests new dinners, which the household accepts or declines with a swipe interface like a dating app. The app builds a weekly dinner plan, pushes it to **Google Calendar**, and generates a **shopping list** for the planned dinners. Shopping lists are kept as a **blog-style history** you can browse by week, month or year. The app can also suggest dinners from a list of ingredients on hand, and offers **quick dinner** ideas. It sends **push notifications** and **Google Chat** messages through a webhook. An **admin page** configures the AI model (LLM).

## 2. Users & access
- [Decided] **One shared household account.** No per-person profiles.
- [Decided] **Anyone in the household can open everything**, including the Admin settings.
- [Design default] The household is shown as a single identity ("Our kitchen · Shared household"). Settings changes apply to everyone.

## 3. Platforms
- [Decided] **Mobile first**, delivered as a **PWA** (installable, with a service worker and app notifications).
- [Decided] **Tablet and desktop layouts** too. On larger screens the focus is **reading ingredients and recipes, managing the calendar, and updating settings**.
- [Design default] Breakpoints: mobile < 768px (bottom nav), tablet 768–1199px (96px icon rail), desktop ≥ 1200px (208px sidebar). Tablet is designed in landscape, for kitchen-counter use.
- [Design default] Works offline for the current plan, shopping list and recipes.

## 4. Navigation
- [Decided] Bottom nav on mobile, five items in this order: **Home · Plan · Discover · Quick · Settings**.
- [Decided] The **Admin page lives inside Settings**. It is not a nav tab.
- [Design default] The Shopping list and Shopping history are reached from Plan and Home (not tabs). The same five items appear in the tablet rail and the desktop sidebar.

## 5. Onboarding — regular dinners
- [Decided] The user enters the dinners the household routinely has ("regulars"). The AI uses these to come up with new suggestions.
- [Design default] Three steps; step 2 is "What's on your usual rotation?" with free-text entry, removable chips and suggested common dinners. Step 3 asks for notification permission.
- [Design default] Regulars can be edited later in Settings → Regular dinners.

## 6. AI dinner suggestions (Discover / swipe)
- [Decided] The AI suggests **new dinners to try** based on the household's regulars.
- [Decided] **Swipe right / "Try it" = accept; swipe left / "Pass" = decline.** Buttons and swipe gestures both work.
- [Decided] **Accepted suggestions go into a "to try" pool** (named the **Quest log**) and get scheduled later, not right away.
- [Decided] Each suggestion card shows: **photo**, **why it was suggested** (referencing the household's regulars), and **difficulty**.
- [Decided] Chosen layout: **stacked photo cards** (mockup 1a).
- [Design default] Declined suggestions are recorded so they aren't suggested again. A "Quest log · N" counter is shown on the screen. Suggestions arrive in batches; a push or Chat message announces new ones.

### Required animations [Decided]
- **Try it:** the card **wiggles, then flies off the right side** of the screen.
- **Pass:** the card **folds over and falls off to the left** of the screen.
- Exact timings and easing are in the developer handoff README. Summary: "Try it!" stamp pops in → ±4° wiggle (420ms) → small pull-back → fly right with a 28° tilt (480ms). Pass: folds on its left edge (rotateY to −80°, shading darkens) → drops and tumbles off left (720ms). The cards behind move forward afterward (300ms). Dragging past 110px triggers the same exits; otherwise the card springs back.
- [Design default] Respect the device's reduced-motion setting (simple fade instead).

## 7. Weekly plan
- [Decided] The **AI builds the week**, mixing **regulars and Quest-log meals**; the **user then adjusts** it (swaps days).
- [Decided] **No fixed rule** for how many new vs. regular dinners go in a week.
- [Decided] The plan covers **dinners** (one per day).
- [Decided] Chosen mobile layout: **draft board** (mockup 1f). It shows an AI note ("I mixed 5 of your regulars with 2 from your Quest log…"), a 2-column grid of day tiles with new dinners highlighted, tap a day to swap, and a "Looks good — push to Calendar" button.
- [Design default] Tablet and desktop: a 7-column week view; drag a dinner from the Quest log or Regulars panel onto a day to swap it in. Includes week navigation, "Rebuild week" and "Push to Calendar".
- [Design default] Crock-pot dinners show a "Start by [time]" hint.

## 8. Google Calendar
- [Decided] The weekly schedule is **placed in a Google Calendar**.
- [Decided] **The app is the source of truth and pushes to Google Calendar (one-way).** Edits made in Google Calendar do not come back into the app.
- [Design default] Connect once with Google sign-in (OAuth) in Settings → Google Calendar; one calendar event per dinner; sync status is shown ("Synced to Google Calendar 2 min ago").

## 9. Shopping list
- [Decided] Generated from the **ingredients needed for the dinners on the schedule**.
- [Decided] **Grouped by dinner** (not by store aisle).
- [Decided] Pantry / on-hand handling: **the user unchecks items they already have** on the list. There is **no separate pantry inventory**.
- [Design default] Checked = needs buying; unchecked = "have it" (struck through, left out of the count). Shows a count like "19 / 23" and can be sent to Google Chat.

## 10. Shopping history (blog format)
- [Decided] Shopping lists are shown in a **blog format**: **each weekly shopping list is a new post**.
- [Decided] Browse by **week / month / year** for historical reference.
- [Design default] Each post shows a week label ("Week 41 · Oct 5 – 11"), an AI-written title, the number of dinners and items, how many items were already on hand, and dinner chips. Past posts are read-only.

## 11. Cook from what I have (ingredients → ideas)
- [Decided] Generate **dinner ideas from a list of available ingredients**.
- [Design default] Ingredient chip input, an "Any time / Quick only" filter, and a "Find ideas" button. Results show how many of your ingredients each idea uses and what's missing, with "Cook tonight" and "Add to Quest log" actions.

## 12. Quick dinners
- [Decided] A way to generate **quick dinner ideas**. "Quick" means **cooked in a crock pot OR 30 minutes or less from prep to table**.
- [Decided] Available in **two places**: a **dedicated entry point** (Quick nav tab plus a Home shortcut) **and** as a **"Quick" filter** on suggestions / ingredient search.
- [Decided] Shown as **two tabs: Crock pot | 30 min or less**.
- [Design default] Each result shows time and difficulty, has a "+" to add to the Quest log, and a "Show me more" option.

## 13. Recipe / dinner detail
- [Decided] A recipe / dinner detail screen.
- [Design default] Photo, tags (Regular/Side quest, difficulty, time), "Why it was picked", ingredients with quantities, steps, "Add to this week", and a favorite (heart) button.
- [Design default] Tablet **kitchen reading mode**: large text, ingredient checkboxes ("3 of 9 set out"), current step highlighted, Previous/Next step buttons, a **Keep screen on** toggle and a text-size control.

## 14. Notifications (push + Google Chat)
- [Decided] The PWA sends **app (push) notifications**.
- [Decided] The app sends **messages to Google Chat using webhooks**.
- [Decided] Events that trigger messages: **New weekly plan ready · Shopping list ready · Day-of dinner reminder · New suggestions to swipe.**
- [Design default] Push and Chat can be switched on or off **separately for each event**. The day-of reminder time can be changed (default 4:30 pm). Settings include the webhook URL, connection status and a "Send test" button.
- [Design default] On iOS, push only works once the app is installed to the home screen, so show an install hint.

## 15. Admin — LLM configuration (inside Settings)
- [Decided] An admin page for configuring the LLM the app uses, including:
  - **Provider and model picker**
  - **API key management**
  - **Temperature / creativity**
  - **System prompt editor**
- [Design default] The API key is stored on the server and only the last 4 characters are shown, with a "Replace" button and "Verified" date. Temperature slider from 0 to 1, labeled "Familiar ↔ Adventurous". Reset and Save buttons.
- Not requested: usage/cost tracking, test playground, global dietary restrictions.

## 16. Settings home
- [Design default] Sections: Household (Regular dinners, Google Calendar connection), Alerts (Notifications & Google Chat), Admin (AI assistant). Desktop shows Notifications, Webhook and AI assistant together on one page.

## 17. Visual design
- [Decided] Design system: **Organic** (warm, rounded, playful; pill-shaped buttons and inputs; soft shadows; generous spacing; photos softened and slightly desaturated).
- [Decided] Color palette **"B4": Grape + teal blue on an off-white background.**
  - Background `#f9f6f1`, surface `#eee9de`, text `#201e1d`
  - Primary (Grape) `#5a358c`; text on grape is always light `#f9f6f1`
  - Secondary (Teal blue) `#29889c`
  - Full light-to-dark scales are in the handoff `tokens.css`.
- [Decided] Heading font: **Bagel Fat One** (Google Fonts). Body font: **Figtree**.
- [Design default] Lucide icons with heavy strokes (2.75).
- [Decided] **Copy tone: playful and quest-themed.** Terms: *Regulars* (routine dinners), *Side quests* (AI suggestions), *Quest log* (to-try pool), *Supply run* (current shopping list), *Supply archive* (history).

## 18. Navigation animation
- [Decided] The **purple pill in the bottom nav slides to the selected menu item**.
- [Design default] 420ms with a slight overshoot; the other icons glide to their new spots; the label fades in after 120ms.

## 19. Accessibility [Design default]
- Swiping is never the only way to act: there are always buttons, and keyboard ←/→ arrows work.
- Text contrast of at least 4.5:1; tap targets of at least 44px; toggles and checkboxes are announced correctly to screen readers.

## 20. Suggested tech stack [Design default]
React + TypeScript + Vite, vite-plugin-pwa, React Router, TanStack Query, Web Animations API or Framer Motion, lucide-react. The backend handles LLM calls, Google Calendar sign-in and sync, Google Chat webhook posts, and push notifications.

## 21. Core data (summary)
- **Dinner**: name, photo, minutes, method (stovetop/oven/crock pot/other), difficulty, ingredients (name and quantity), steps, source (regular or suggestion).
- **Suggestion**: dinner, reason ("why this?"), status (pending/accepted/declined).
- **Week plan**: week start date, one dinner per day (flagged if new), status (draft/synced), last calendar sync time.
- **Shopping list**: week, items grouped by dinner (each marked needed or have-it), when it was sent to Chat, AI-written post title.
- **Settings**: Push/Chat switch for each event, reminder time, webhook URL, calendar, AI settings (provider, model, temperature, system prompt, last 4 of API key, verified date).
- A dinner counts as quick if it is a crock-pot meal or takes 30 minutes or less.

## 22. Not yet designed / open items
- Empty, loading and error states (suggested patterns are in the handoff README).
- The swap picker for a mobile plan day (suggested: a bottom sheet listing the Quest log and Regulars).
- Settings → Household and Regular dinners management screens.
- Onboarding steps 1 and 3.
- A tablet version of Settings, and portrait tablet layouts.
