import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Link, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { HomePage } from '../home/HomePage.tsx'
import { PlanPage } from '../plan/PlanPage.tsx'
import { createNetworkStatus, type NetworkWindow } from '../pwa/networkStatus.ts'
import { RegularsSettingsPage } from '../settings/RegularsSettingsPage.tsx'
import { SAVED_KEY } from '../shopping/offline.ts'
import { SupplyRunPage } from '../shopping/SupplyRunPage.tsx'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody, recipeBody, type DinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'

const WEEK = '2026-10-05'
const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const HELD = new Set(['/api/v1/home', `/api/v1/plans/${WEEK}`, `/api/v1/shopping/${WEEK}`, '/api/v1/dinners/d1'])

const TACOS = dinnerBody('d1', 'Beef tacos', {
  method: 'stovetop',
  minutes_total: 30,
  photo_url: '/media/photos/p1-400.jpg',
  meal_source: 'cook',
})

const PLAN = {
  id: 'plan-1',
  week_start: WEEK,
  status: 'draft',
  ai_note: null,
  calendar_synced_at: null,
  days: DAYS.map((date, i) => ({
    date,
    dinner: {
      id: i === 0 ? 'd1' : `d${i + 1}`,
      name: i === 0 ? 'Beef tacos' : `Dinner ${i + 1}`,
      status: 'regular',
      photo: i === 0 ? '/media/photos/p1-400.jpg' : null,
      minutes: 30,
      method: 'stovetop',
      meal_source: 'cook',
    },
    is_new: false,
    start_by: null,
    meal_source: 'cook',
  })),
}

const HOME = {
  dateLabel: 'Monday, Oct 5',
  timezone: 'America/New_York',
  tonight: {
    id: 'd1',
    name: 'Beef tacos',
    status: 'regular',
    photo: '/media/photos/p1-400.jpg',
    origin: 'regular',
    minutes_total: 30,
    meal_source: 'cook',
  },
  manualTonight: null,
  evening: null,
  sideQuestCount: 0,
  shoppingList: { weekStart: WEEK, label: 'Week 41 · Oct 5 – 11', ready: true, sentToChatAt: null },
  recentUpdates: { unread: false },
}

const RUN = {
  weekStart: WEEK,
  label: 'Week 41 · Oct 5 – 11',
  title: null,
  status: 'current',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 1,
  total_count: 1,
  groups: [
    {
      dinner: { id: 'd1', name: 'Beef tacos', status: 'regular', meal_source: 'cook' },
      date: WEEK,
      day: 'Monday',
      items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }],
    },
  ],
}

const RECIPE = recipeBody('d1', 'Beef tacos', {
  photo_urls: { 400: '/media/photos/p1-400.jpg', 1200: '/media/photos/p1-1200.jpg' },
  meal_source: 'cook',
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(window, 'matchMedia')
  localStorage.clear()
})

// renderLoaded opens Home, the plan, the Supply run and the dinner, each
// already read as a cooked Beef tacos, then the Regulars edit sheet.
async function renderLoaded() {
  const regulars: DinnerBody[] = [TACOS]
  let hold = false
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input)
    const method = init?.method ?? 'GET'
    const key = `${method} ${path}`
    if (hold && method === 'GET' && HELD.has(path)) return new Promise<Response>(() => {})
    if (key === 'GET /api/v1/me') return jsonResponse(200, ME)
    if (key === 'GET /api/v1/settings/household') return jsonResponse(200, HOUSEHOLD_SETTINGS)
    if (key === 'GET /api/v1/home') return jsonResponse(200, HOME)
    if (key === 'GET /api/v1/rotation-reviews') return jsonResponse(200, { reviews: [] })
    if (key === 'GET /api/v1/updates') return jsonResponse(200, { updates: [] })
    if (key === `GET /api/v1/plans/${WEEK}`) return jsonResponse(200, PLAN)
    if (key === `GET /api/v1/shopping/${WEEK}`) return jsonResponse(200, RUN)
    if (key === 'GET /api/v1/dinners/d1') return jsonResponse(200, RECIPE)
    if (key === 'GET /api/v1/dinners?status=regular') return jsonResponse(200, { dinners: regulars })
    if (key === 'GET /api/v1/dinners?status=quest_log') return jsonResponse(200, { dinners: [] })
    if (key.startsWith('GET /api/v1/plans/')) return errorResponse(404, 'not_found')
    if (key === 'PATCH /api/v1/dinners/d1') {
      return jsonResponse(409, {
        error: {
          code: 'planned',
          message: 'Beef tacos is on the plan and must be removed from the plan first',
          retryable: false,
        },
      })
    }
    throw new Error(`unexpected fetch: ${key}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  mockViewport(1280)
  const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/">
          <nav>
            <Link to="/">Home</Link>
            <Link to={`/plan?week=${WEEK}`}>Plan</Link>
            <Link to={`/plan/shopping?week=${WEEK}`}>Supply run</Link>
            <Link to="/dinner/d1">Dinner</Link>
            <Link to="/settings/regulars">Regulars</Link>
          </nav>
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="/plan" element={<PlanPage />} />
            <Route path="/plan/shopping" element={<SupplyRunPage />} />
            <Route path="/dinner/:id" element={<DinnerDetailPage />} />
            <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )

  const tonight = await screen.findByRole('region', { name: 'Tonight' })
  expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
  expect(within(tonight).getByRole('img', { name: 'Beef tacos' }).tagName).toBe('IMG')

  fireEvent.click(screen.getByRole('link', { name: 'Plan' }))
  const week = await screen.findByRole('list', { name: 'Dinners this week' })
  expect(within(week).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()

  fireEvent.click(screen.getByRole('link', { name: 'Supply run' }))
  const group = await screen.findByRole('region', { name: 'Beef tacos' })
  expect(within(group).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()

  fireEvent.click(screen.getByRole('link', { name: 'Dinner' }))
  expect(await screen.findByRole('region', { name: 'Ingredients' })).toBeInTheDocument()

  fireEvent.click(screen.getByRole('link', { name: 'Regulars' }))
  await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
  return { fetchMock, holdReads: () => { hold = true } }
}

describe('a Delivery edit of a dinner that is on the plan', () => {
  it('shows the refusal and leaves Home, the plan and the Supply run as they were', async () => {
    const { holdReads } = await renderLoaded()

    fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.click(sheet.getByRole('radio', { name: 'Delivery' }))
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))
    expect(await sheet.findByRole('alert')).toHaveTextContent('Remove Beef tacos from the plan first.')
    holdReads()

    fireEvent.click(screen.getByRole('link', { name: 'Home' }))
    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(within(tonight).getByText('Beef tacos')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('link', { name: 'Plan' }))
    const week = await screen.findByRole('list', { name: 'Dinners this week' })
    const monday = within(week).getByRole('listitem', { name: 'Monday' })
    expect(within(monday).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()
    expect(within(monday).queryByText('Nothing planned')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('link', { name: 'Supply run' }))
    expect(await screen.findByRole('region', { name: 'Beef tacos' })).toBeInTheDocument()
    expect(screen.getByText('Ground beef')).toBeInTheDocument()
  })
})

const ARCHIVE_WEEK = '2026-09-28'

const PAST_DAYS = ['2026-09-28', '2026-09-29', '2026-09-30', '2026-10-01', '2026-10-02', '2026-10-03', '2026-10-04']

function planFor(week: string, dates: string[], monday: { id: string; name: string }) {
  return {
    ...PLAN,
    id: `plan-${week}`,
    week_start: week,
    days: dates.map((date, i) => ({
      date,
      dinner:
        i === 0
          ? { id: monday.id, name: monday.name, status: 'regular', photo: null, minutes: 30, method: 'stovetop', meal_source: 'cook' }
          : { id: `x${week}-${i}`, name: `Other ${i}`, status: 'regular', photo: null, minutes: 30, method: 'stovetop', meal_source: 'cook' },
      is_new: false,
      start_by: null,
      meal_source: 'cook',
    })),
  }
}

describe('a Delivery edit when the current plan no longer has the dinner', () => {
  it('leaves both plan caches intact and drops the dinner only from the saved run of that plan', async () => {
    const current = {
      ...RUN,
      needed_count: 2,
      total_count: 2,
      groups: [
        RUN.groups[0],
        {
          dinner: { id: 'd4', name: 'Pot roast', status: 'regular', meal_source: 'cook' },
          date: '2026-10-06',
          day: 'Tuesday',
          items: [{ id: 'i2', name: 'Chuck roast', qty: '3 lb', needed: true }],
        },
      ],
    }
    const archived = {
      ...RUN,
      weekStart: ARCHIVE_WEEK,
      label: 'Week 40 · Sep 28 – Oct 4',
      status: 'archived',
    }
    localStorage.setItem(
      SAVED_KEY,
      JSON.stringify({
        schedule: {
          timezone: HOUSEHOLD_SETTINGS.timezone,
          plan_draft_dow: HOUSEHOLD_SETTINGS.plan_draft_dow,
          plan_draft_time: HOUSEHOLD_SETTINGS.plan_draft_time,
        },
        runs: { [WEEK]: current, [ARCHIVE_WEEK]: archived },
        queue: [],
      }),
    )

    let onLine = true
    const win = Object.assign(new EventTarget(), {
      navigator: {
        get onLine() {
          return onLine
        },
      },
    }) as NetworkWindow & EventTarget
    const regulars: DinnerBody[] = [TACOS]
    const currentPlan = planFor(WEEK, DAYS, { id: 'd4', name: 'Pot roast' })
    const pastPlan = planFor(ARCHIVE_WEEK, PAST_DAYS, { id: 'd1', name: 'Beef tacos' })
    let holdPlans = false
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (!onLine) throw new TypeError('offline')
      const path = String(input)
      const method = init?.method ?? 'GET'
      const key = `${method} ${path}`
      if (holdPlans && key.startsWith('GET /api/v1/plans/')) return new Promise<Response>(() => {})
      if (key === 'GET /api/v1/me') return jsonResponse(200, ME)
      if (key === 'GET /api/v1/settings/household') return jsonResponse(200, HOUSEHOLD_SETTINGS)
      if (key === 'GET /api/v1/home') return jsonResponse(200, { ...HOME, tonight: null })
      if (key === 'GET /api/v1/rotation-reviews') return jsonResponse(200, { reviews: [] })
      if (key === 'GET /api/v1/updates') return jsonResponse(200, { updates: [] })
      if (key === `GET /api/v1/plans/${WEEK}`) return jsonResponse(200, currentPlan)
      if (key === `GET /api/v1/plans/${ARCHIVE_WEEK}`) return jsonResponse(200, pastPlan)
      if (key.startsWith('GET /api/v1/plans/')) return errorResponse(404, 'not_found')
      if (key.startsWith('GET /api/v1/shopping/')) return new Promise<Response>(() => {})
      if (key === 'GET /api/v1/dinners?status=regular') return jsonResponse(200, { dinners: regulars })
      if (key === 'GET /api/v1/dinners?status=quest_log') return jsonResponse(200, { dinners: [] })
      if (key === 'PATCH /api/v1/dinners/d1') {
        const currentDinner = regulars[0]
        if (currentDinner === undefined) throw new Error('no Regular to edit')
        const body = JSON.parse(String(init?.body)) as { meal_source: DinnerBody['meal_source'] }
        const edited: DinnerBody = { ...currentDinner, meal_source: body.meal_source }
        regulars[0] = edited
        return jsonResponse(200, edited)
      }
      throw new Error(`unexpected fetch: ${key}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    mockViewport(1280)
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(win), createVerifiedSession(localStorage))
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/settings/regulars">
            <nav>
              <Link to={`/plan?week=${WEEK}`}>This week</Link>
              <Link to={`/plan?week=${ARCHIVE_WEEK}`}>Last week</Link>
              <Link to="/settings/regulars">Regulars</Link>
              <Link to={`/plan/shopping?week=${WEEK}`}>Supply run</Link>
            </nav>
            <Routes>
              <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
              <Route path="/plan" element={<PlanPage />} />
              <Route path="/plan/shopping" element={<SupplyRunPage />} />
            </Routes>
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
    fireEvent.click(screen.getByRole('link', { name: 'This week' }))
    const thisWeek = await screen.findByRole('list', { name: 'Dinners this week' })
    expect(within(within(thisWeek).getByRole('listitem', { name: 'Monday' })).getByRole('button', { name: 'Pot roast' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: 'Last week' }))
    const lastWeek = await screen.findByRole('list', { name: 'Dinners this week' })
    expect(within(within(lastWeek).getByRole('listitem', { name: 'Monday' })).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: 'Regulars' }))
    await screen.findByRole('heading', { level: 2, name: /^On rotation/ })

    fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.click(sheet.getByRole('radio', { name: 'Delivery' }))
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    holdPlans = true

    const saved = JSON.parse(localStorage.getItem(SAVED_KEY) ?? '') as {
      runs: Record<string, { groups: { dinner: { name: string }; items: { name: string }[] }[]; needed_count: number; total_count: number; status: string }>
    }
    expect(saved.runs[WEEK]?.groups.map((group) => group.dinner.name)).toEqual(['Pot roast'])
    expect(saved.runs[WEEK]?.needed_count).toBe(1)
    expect(saved.runs[WEEK]?.total_count).toBe(1)
    expect(saved.runs[ARCHIVE_WEEK]?.status).toBe('archived')
    expect(saved.runs[ARCHIVE_WEEK]?.groups.map((group) => group.dinner.name)).toEqual(['Beef tacos'])
    expect(saved.runs[ARCHIVE_WEEK]?.groups[0]?.items.map((item) => item.name)).toEqual(['Ground beef'])

    fireEvent.click(screen.getByRole('link', { name: 'Last week' }))
    const past = await screen.findByRole('list', { name: 'Dinners this week' })
    expect(within(within(past).getByRole('listitem', { name: 'Monday' })).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: 'This week' }))
    const currentWeek = await screen.findByRole('list', { name: 'Dinners this week' })
    const monday = within(currentWeek).getByRole('listitem', { name: 'Monday' })
    expect(within(monday).getByRole('button', { name: 'Pot roast' })).toBeInTheDocument()
    expect(within(monday).queryByText('Nothing planned')).not.toBeInTheDocument()

    onLine = false
    act(() => {
      win.dispatchEvent(new Event('offline'))
    })
    fireEvent.click(screen.getByRole('link', { name: 'Supply run' }))
    expect(await screen.findByRole('region', { name: 'Pot roast' })).toBeInTheDocument()
    expect(screen.getByText('Chuck roast')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Beef tacos' })).not.toBeInTheDocument()
    expect(screen.queryByText('Ground beef')).not.toBeInTheDocument()
  })
})
