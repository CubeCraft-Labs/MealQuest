import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Link, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { HomePage } from '../home/HomePage.tsx'
import { PlanPage } from '../plan/PlanPage.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { RegularsSettingsPage } from '../settings/RegularsSettingsPage.tsx'
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
      const current = regulars[0]
      if (current === undefined) throw new Error('no Regular to edit')
      const body = JSON.parse(String(init?.body)) as { meal_source: DinnerBody['meal_source'] }
      const edited: DinnerBody = { ...current, meal_source: body.meal_source }
      regulars[0] = edited
      return jsonResponse(200, edited)
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

describe('a Delivery edit already loaded on Home, the plan, the Supply run and the dinner', () => {
  it('drops the photo and the Recipe affordance before those reads return', async () => {
    const { holdReads } = await renderLoaded()

    fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.click(sheet.getByRole('radio', { name: 'Delivery' }))
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    holdReads()

    fireEvent.click(screen.getByRole('link', { name: 'Home' }))
    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
    expect(within(tonight).getByRole('img', { name: 'Beef tacos' }).tagName).toBe('DIV')

    fireEvent.click(screen.getByRole('link', { name: 'Plan' }))
    const week = await screen.findByRole('list', { name: 'Dinners this week' })
    const monday = within(week).getByRole('listitem', { name: 'Monday' })
    expect(within(monday).queryByRole('button', { name: 'Beef tacos' })).not.toBeInTheDocument()
    expect(within(monday).getByRole('img', { name: 'Beef tacos', hidden: true }).tagName).toBe('DIV')

    fireEvent.click(screen.getByRole('link', { name: 'Supply run' }))
    await screen.findByRole('heading', { name: 'Shopping list' })
    expect(screen.queryByRole('button', { name: 'Beef tacos' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('link', { name: 'Dinner' }))
    expect(await screen.findByText(/is not cooked at home/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Ingredients' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Ingredients' })).not.toBeInTheDocument()
  })
})
