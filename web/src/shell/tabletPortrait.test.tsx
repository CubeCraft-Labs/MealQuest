import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppRoutes } from '../App.tsx'
import planStyles from '../plan/PlanPage.module.css'
import settingsTabsStyles from '../settings/SettingsTabs.module.css'
import {
  AI_MODELS,
  AI_SETTINGS,
  CHAT_WEBHOOK_NONE,
  GOOGLE_NOT_CONNECTED,
  HOME,
  HOUSEHOLD_SETTINGS,
  NOTIFICATION_SETTINGS,
  RECIPE_SEARCH_SETTINGS,
  ApiProviders,
} from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody } from '../test/dinners.ts'
import { jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import shellStyles from './AppShell.module.css'
import railStyles from './Rail.module.css'

// Every primary route on a tablet held upright (820 × 1180) and on its side
// (1180 × 820): both have the 96 px rail and the tablet layout; upright, the
// shell is marked portrait, the Plan week reflows four days over three, and
// Settings' two columns stack into one.

const PORTRAIT = { width: 820, height: 1180 }
const LANDSCAPE = { width: 1180, height: 820 }

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']

const PLAN = {
  id: 'plan-1',
  week_start: '2026-10-05',
  week_label: 'Week 41 · Oct 5 – 11',
  status: 'draft',
  ai_note: null,
  calendar_synced_at: null,
  days: DAYS.map((date, i) => ({
    date,
    dinner: { id: `d${i}`, name: `Dinner ${i}`, status: 'regular', photo: null, minutes: 30, method: 'stovetop', origin: 'regular' },
    is_new: false,
    start_by: null,
  })),
  updated_at: '2026-10-04T15:00:00Z',
}

const SUPPLY_RUN = {
  weekStart: '2026-10-05',
  label: 'Week 41 · Oct 5 – 11',
  title: null,
  status: 'current',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 1,
  total_count: 1,
  groups: [
    {
      dinner: { id: 'd0', name: 'Dinner 0', status: 'regular' },
      date: '2026-10-05',
      day: 'Monday',
      items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }],
    },
  ],
}

const DETAIL = {
  id: 'd1',
  name: 'Korean beef bowls',
  status: 'regular',
  origin: 'regular',
  method: 'stovetop',
  minutes_total: 30,
  difficulty: 'easy',
  cuisine: 'korean',
  is_favorite: false,
  photo_urls: null,
  ingredients: [{ name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' }],
  steps: ['Brown the beef.'],
  source: null,
  grounding_status: 'manual',
  grounding_error: null,
  why_picked: null,
}

// The API every route reads, answered as a settled household's server would.
// Any other request stays pending, so a route that reads more just shows its
// loading state.
const API: Record<string, unknown> = {
  '/api/v1/me': ME,
  '/api/v1/home': HOME,
  '/api/v1/rotation-reviews': { reviews: [] },
  '/api/v1/settings/household': HOUSEHOLD_SETTINGS,
  '/api/v1/settings/ai': AI_SETTINGS,
  '/api/v1/settings/ai/models': AI_MODELS,
  '/api/v1/settings/recipe-search': RECIPE_SEARCH_SETTINGS,
  '/api/v1/settings/notifications': NOTIFICATION_SETTINGS,
  '/api/v1/settings/chat-webhook': CHAT_WEBHOOK_NONE,
  '/api/v1/integrations/google': GOOGLE_NOT_CONNECTED,
  '/api/v1/plans/2026-10-05': PLAN,
  '/api/v1/shopping/2026-10-05': SUPPLY_RUN,
  '/api/v1/shopping/archive?by=week': { by: 'week', periods: [] },
  '/api/v1/suggestions/deck': { suggestions: [], active_batch: null, failed_batch: null },
  '/api/v1/dinners?status=regular': { dinners: [dinnerBody('r1', 'Beef tacos')] },
  '/api/v1/dinners?status=quest_log': { dinners: [] },
  '/api/v1/dinners/d1': DETAIL,
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
  const path = (input: Parameters<typeof fetch>[0]) => new URL(String(input), 'https://mealquest.test')
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>((input) => {
      const url = path(input)
      const key = `${url.pathname}${url.search}`
      if (!Object.hasOwn(API, key)) return new Promise<Response>(() => {})
      return Promise.resolve(jsonResponse(200, API[key]))
    }),
  )
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function renderAt(path: string, { width, height }: { width: number; height: number }) {
  mockViewport(width, { height })
  render(
    <InDataRouter path={path}>
      <ApiProviders>
        <AppRoutes />
      </ApiProviders>
    </InDataRouter>,
  )
  // The routes wait for the household settings (OnboardingGate).
  await screen.findByRole('navigation', { name: 'Primary' })
}

function shell() {
  return screen.getByRole('main').parentElement!
}

const ROUTES: [string, string][] = [
  ['Home', '/'],
  ['Plan', '/plan?week=2026-10-05'],
  ['Supply run', '/plan/shopping'],
  ['Supply archive', '/plan/shopping/archive'],
  ['Discover', '/discover'],
  ['Quick dinners', '/quick'],
  ['Cook from what I have', '/cook-from-pantry'],
  ['Dinner detail (Kitchen reading mode)', '/dinner/d1'],
  ['Settings', '/settings'],
  ['Notifications', '/settings/notifications'],
  ['AI assistant', '/settings/ai'],
  ['Household', '/settings/household'],
  ['Regulars', '/settings/regulars'],
  ['Calendar', '/settings/calendar'],
]

describe.each([
  ['portrait', PORTRAIT],
  ['landscape', LANDSCAPE],
])('tablet %s', (orientation, viewport) => {
  const portrait = orientation === 'portrait'

  it.each(ROUTES)(`renders %s with the 96 px rail in the tablet ${orientation} layout`, async (_name, path) => {
    await renderAt(path, viewport)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
    expect(screen.getByRole('navigation', { name: 'Primary' })).toHaveClass(railStyles.rail!)
    expect(declaredStyle(screen.getByRole('navigation', { name: 'Primary' })).width).toBe('96px')
    expect(shell()).toHaveClass(shellStyles.tablet!)
    if (portrait) expect(shell()).toHaveClass(shellStyles.portrait!)
    else expect(shell()).not.toHaveClass(shellStyles.portrait!)
    expect(await screen.findByRole('heading', { level: 1 })).toBeInTheDocument()
  })

  it(`lays the Plan week out ${portrait ? 'four days over three' : 'in seven columns'}`, async () => {
    await renderAt('/plan?week=2026-10-05', viewport)
    const week = await screen.findByRole('list', { name: 'Dinners this week' })
    const page = week.closest(`.${planStyles.tabletPage}`)
    expect(page).not.toBeNull()
    if (portrait) expect(page).toHaveClass(planStyles.portraitPage!)
    else expect(page).not.toHaveClass(planStyles.portraitPage!)
    expect(declaredStyle(week).gridTemplateColumns).toBe(
      portrait ? 'repeat(4, minmax(0, 1fr))' : 'repeat(7, minmax(112px, 1fr))',
    )
    expect(declaredStyle(week).overflowX).toBe('auto')
  })

  it(`shows Settings (mock 2e) in ${portrait ? 'one column' : 'two columns'}`, async () => {
    await renderAt('/settings', viewport)
    const panel = screen.getByRole('tabpanel', { name: 'Notifications & AI' })
    const page = panel.closest(`.${settingsTabsStyles.page}`)
    if (portrait) expect(page).toHaveClass(settingsTabsStyles.portrait!)
    else expect(page).not.toHaveClass(settingsTabsStyles.portrait!)
    expect(declaredStyle(panel).gridTemplateColumns).toBe(
      portrait ? 'minmax(0, 1fr)' : 'minmax(0, 1fr) minmax(0, 1.15fr)',
    )
  })
})

it('reflows when the tablet turns', async () => {
  const resize = mockViewport(PORTRAIT.width, { height: PORTRAIT.height })
  render(
    <InDataRouter path="/plan?week=2026-10-05">
      <ApiProviders>
        <AppRoutes />
      </ApiProviders>
    </InDataRouter>,
  )
  const week = await screen.findByRole('list', { name: 'Dinners this week' })
  expect(shell()).toHaveClass(shellStyles.portrait!)
  expect(week.closest(`.${planStyles.tabletPage}`)).toHaveClass(planStyles.portraitPage!)
  resize(LANDSCAPE.width, LANDSCAPE.height)
  expect(shell()).toHaveClass(shellStyles.tablet!)
  expect(shell()).not.toHaveClass(shellStyles.portrait!)
  expect(week.closest(`.${planStyles.tabletPage}`)).not.toHaveClass(planStyles.portraitPage!)
})
