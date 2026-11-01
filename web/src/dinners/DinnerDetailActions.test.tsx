import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { PlanPage } from '../plan/PlanPage.tsx'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'

const DINNER = '/api/v1/dinners/d1'
const FAVORITE = `${DINNER}/favorite`
const THIS_WEEK = '/api/v1/plans/2026-10-05'
const NEXT_WEEK = '/api/v1/plans/2026-10-12'
const THURSDAY = `${THIS_WEEK}/days/2026-10-08`

// detail is the Korean beef bowls, a Quest-log Side quest, as GET
// /api/v1/dinners/{id} sends it.
function detail(overrides: Record<string, unknown> = {}) {
  return {
    id: 'd1',
    name: 'Korean beef bowls',
    status: 'quest_log',
    origin: 'suggested',
    method: 'stovetop',
    minutes_total: 30,
    difficulty: 'easy',
    cuisine: 'korean',
    is_favorite: false,
    meal_source: 'cook',
    photo_urls: null,
    ingredients: [{ name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' }],
    steps: ['Brown the beef with the sauce.'],
    source: null,
    grounding_status: 'grounded',
    grounding_error: null,
    why_picked: null,
    ...overrides,
  }
}

// listed is the bowls as the dinners list, and so the favorite toggle,
// answers: no recipe.
function listed(is_favorite: boolean) {
  return {
    id: 'd1',
    name: 'Korean beef bowls',
    method: 'stovetop',
    minutes_total: 30,
    difficulty: 'easy',
    is_favorite,
    photo_url: null,
    grounding_status: 'grounded',
    grounding_error: null,
    photo_backfill_eligible: false,
    meal_source: 'cook',
  }
}

const THIS_NAMES = ['Beef tacos', 'Spaghetti bolognese', 'Lemon orzo', 'Chicken stir-fry', 'Pizza night', 'Fish tacos', 'Sheet-pan salmon']
const NEXT_NAMES = ['Chili', 'Pad thai', 'Burgers', 'Ramen', 'Pizza night', 'Pot roast', 'Fried rice']

// planBody is a week's plan as GET /api/v1/plans/{weekStart} sends it, with
// dinner ids p0 to p6 unless ids says otherwise.
function planBody(weekStart: string, names: string[], ids = names.map((_, i) => `p${i}`)) {
  const start = new Date(`${weekStart}T00:00:00Z`).getTime()
  return {
    id: `plan-${weekStart}`,
    week_start: weekStart,
    status: 'draft',
    ai_note: null,
    calendar_synced_at: null as string | null,
    days: names.map((name, i) => ({
      date: new Date(start + i * 86_400_000).toISOString().slice(0, 10),
      dinner: { id: ids[i], name, status: 'regular', photo: null, minutes: 30, method: 'stovetop', meal_source: 'cook' },
      is_new: false,
      start_by: null,
      meal_source: 'cook' as const,
    })),
  }
}

// THURSDAY_SWAPPED is this week's plan as the swap of Thursday answers it.
function thursdaySwapped() {
  const ids = THIS_NAMES.map((_, i) => (i === 3 ? 'd1' : `p${i}`))
  const names = THIS_NAMES.map((name, i) => (i === 3 ? 'Korean beef bowls' : name))
  const plan = planBody('2026-10-05', names, ids)
  plan.days[3]!.is_new = true
  plan.days[3]!.dinner.status = 'quest_log'
  return plan
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

// stubServer answers "METHOD path" requests from responders, with /me, the
// household settings and the dinner; any other request fails the test.
function stubServer(responders: Record<string, Responder> = {}) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    [`GET ${DINNER}`]: () => jsonResponse(200, detail()),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond(init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// sent lists the requests sent with method, as "path body".
function sent(fetchMock: Mock<typeof fetch>, method: string) {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === method)
    .map(([input, init]) => `${String(input)} ${String(init?.body)}`)
}

function reads(fetchMock: Mock<typeof fetch>, path: string) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === path).length
}

// deferred is a response the test sends when it chooses.
function deferred() {
  let send!: (response: Response) => void
  const response = new Promise<Response>((resolve) => {
    send = resolve
  })
  return { response, send }
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the bowls' detail screen, with plan for the Plan: a probe unless told
// otherwise. It answers with the router.
async function renderPage(plan = <h1>Plan probe</h1>) {
  const api = createApiClient(fakeLocation('/dinner/d1'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const router = createMemoryRouter(
    [
      { path: '/dinner/:id', element: <DinnerDetailPage /> },
      { path: '/plan', element: plan },
    ],
    { initialEntries: ['/dinner/d1'] },
  )
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ApiProvider>,
  )
  await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
  return router
}

// openSheet opens Add to this week and waits for the week's days.
async function openSheet(listName = 'Days of this week') {
  fireEvent.click(screen.getByRole('button', { name: 'Add to this week' }))
  const sheet = screen.getByRole('dialog', { name: 'Add Korean beef bowls' })
  const days = await within(sheet).findByRole('list', { name: listName })
  return { sheet, days }
}

// dayRows is each day's row text, as "Monday 5 · Beef tacos · Stovetop · 30 min",
// with plain spaces.
function dayRows(days: HTMLElement) {
  return within(days)
    .getAllByRole('listitem')
    .map((row) => [...row.querySelectorAll('span')].filter((span) => span.children.length === 0 && !span.closest('[aria-hidden="true"]')).map((span) => span.textContent).join(' · ').replace(/\s/g, ' '))
}

beforeEach(() => {
  mockViewport(390)
  // Wednesday Oct 7, noon in New York.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
})

afterEach(() => {
  vi.useRealTimers()
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

describe('Add to this week', () => {
  it('lists the seven days of this week with their dinners, and next week by the toggle', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('2026-10-05', THIS_NAMES)),
      [`GET ${NEXT_WEEK}`]: () => jsonResponse(200, planBody('2026-10-12', NEXT_NAMES)),
    })
    await renderPage()
    // Nothing about the week is read until the sheet opens.
    expect(reads(fetchMock, THIS_WEEK)).toBe(0)

    const { sheet, days } = await openSheet()
    expect(within(sheet).getByText("Oct 5 – 11 · replaces the day's dinner")).toBeInTheDocument()
    expect(dayRows(days)).toEqual([
      'Monday 5 · Beef tacos · Stovetop · 30 min',
      'Tuesday 6 · Spaghetti bolognese · Stovetop · 30 min',
      'Wednesday 7 · Lemon orzo · Stovetop · 30 min',
      'Thursday 8 · Chicken stir-fry · Stovetop · 30 min',
      'Friday 9 · Pizza night · Stovetop · 30 min',
      'Saturday 10 · Fish tacos · Stovetop · 30 min',
      'Sunday 11 · Sheet-pan salmon · Stovetop · 30 min',
    ])
    expect(within(days).getAllByRole('button')).toHaveLength(7)

    fireEvent.click(within(sheet).getByRole('radio', { name: 'Next week' }))
    const next = await within(sheet).findByRole('list', { name: 'Days of next week' })
    expect(within(sheet).getByText("Oct 12 – 18 · replaces the day's dinner")).toBeInTheDocument()
    expect(dayRows(next)[0]).toBe('Monday 12 · Chili · Stovetop · 30 min')
    expect(dayRows(next)[6]).toBe('Sunday 18 · Fried rice · Stovetop · 30 min')

    fireEvent.click(within(sheet).getByRole('radio', { name: 'This week' }))
    expect(await within(sheet).findByRole('list', { name: 'Days of this week' })).toBeInTheDocument()
  })

  it("takes this week in the household's timezone, not the device's UTC date", async () => {
    // Sunday Oct 11, 10 pm in New York, already Monday Oct 12 in UTC.
    vi.setSystemTime(new Date('2026-10-12T02:00:00Z'))
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('2026-10-05', THIS_NAMES)) })
    await renderPage()

    const { days } = await openSheet()
    expect(dayRows(days)[6]).toBe('Sunday 11 · Sheet-pan salmon · Stovetop · 30 min')
    expect(reads(fetchMock, THIS_WEEK)).toBe(1)
    expect(reads(fetchMock, NEXT_WEEK)).toBe(0)
  })

  it('swaps the day chosen through the plan API, confirms it and reads the plan again', async () => {
    const swap = deferred()
    let plan = planBody('2026-10-05', THIS_NAMES)
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`PUT ${THURSDAY}`]: () => swap.response,
    })
    await renderPage()
    const { days } = await openSheet()

    fireEvent.click(within(days).getByRole('button', { name: /Thursday 8/ }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(sent(fetchMock, 'PUT')).toHaveLength(1))
    expect(sent(fetchMock, 'PUT')).toEqual([`${THURSDAY} {"dinner_id":"d1"}`])
    expect(screen.getByRole('status')).toHaveTextContent('Adding to Thursday…')

    plan = thursdaySwapped()
    await act(async () => swap.send(jsonResponse(200, plan)))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Added to Thursday this week.'))
    // The plan query is invalidated: the week is read again from the server.
    await waitFor(() => expect(reads(fetchMock, THIS_WEEK)).toBe(2))

    // The sheet opened again shows the plan as swapped.
    const again = await openSheet()
    expect(dayRows(again.days)[3]).toBe('Thursday 8 · Korean beef bowls · Stovetop · 30 min · This dinner')
    expect(within(again.sheet).getByText('Korean beef bowls is already on Thursday this week.')).toBeInTheDocument()
    expect(within(again.days).queryAllByRole('button')).toHaveLength(0)
  })

  it('offers to draft a week with no plan on the Plan, and swaps nothing', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => errorResponse(404, 'not_found') })
    await renderPage()

    fireEvent.click(screen.getByRole('button', { name: 'Add to this week' }))
    const sheet = screen.getByRole('dialog', { name: 'Add Korean beef bowls' })
    const draft = await within(sheet).findByRole('link', { name: 'Draft this week first' })
    expect(draft).toHaveAttribute('href', '/plan')
    expect(within(sheet).getByText("There's no plan for this week yet, so there's no day to add it to.")).toBeInTheDocument()
    expect(within(sheet).queryByRole('list')).not.toBeInTheDocument()

    fireEvent.click(draft)
    expect(await screen.findByRole('heading', { name: 'Plan probe' })).toBeInTheDocument()
    expect(sent(fetchMock, 'PUT')).toEqual([])
  })

  it('shows a refused swap with the error, puts the day back and retries it', async () => {
    let refuse = true
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('2026-10-05', THIS_NAMES)),
      [`PUT ${THURSDAY}`]: () => (refuse ? errorResponse(500, 'internal') : jsonResponse(200, thursdaySwapped())),
    })
    await renderPage()
    const { days } = await openSheet()
    fireEvent.click(within(days).getByRole('button', { name: /Thursday 8/ }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't add it to Thursday this week. internal message")
    const reopened = await openSheet()
    expect(dayRows(reopened.days)[3]).toBe('Thursday 8 · Chicken stir-fry · Stovetop · 30 min')
    fireEvent.keyDown(reopened.sheet, { key: 'Escape' })

    refuse = false
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Added to Thursday this week.'))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(sent(fetchMock, 'PUT')).toEqual([`${THURSDAY} {"dinner_id":"d1"}`, `${THURSDAY} {"dinner_id":"d1"}`])
  })

  it("leaves a push's Supply run outcome behind on the Plan, as a swap on the Draft board does", async () => {
    let plan = planBody('2026-10-05', THIS_NAMES)
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`POST ${THIS_WEEK}/push-calendar`]: () => {
        plan = { ...plan, status: 'synced', calendar_synced_at: '2026-10-07T16:00:00Z' }
        return jsonResponse(200, { ...plan, supply_run: { status: 'generated', error: null } })
      },
      [`PUT ${THURSDAY}`]: () => {
        plan = { ...thursdaySwapped(), status: 'changed', calendar_synced_at: '2026-10-07T16:00:00Z' }
        return jsonResponse(200, plan)
      },
      'GET /api/v1/shopping/2026-10-05': () => errorResponse(404, 'not_found'),
    })
    const router = await renderPage(<PlanPage />)

    // The week is pushed on the Plan, which says its Supply run was made.
    await act(() => router.navigate('/plan'))
    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByRole('status')).toHaveTextContent("Made this week's Supply run from the plan.")
    // Coming back from the dinner with nothing swapped, it still says so.
    await act(() => router.navigate('/dinner/d1'))
    await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
    await act(() => router.navigate('/plan'))
    expect(await screen.findByRole('status')).toHaveTextContent("Made this week's Supply run from the plan.")

    // Thursday is swapped from the dinner's Add to this week.
    await act(() => router.navigate('/dinner/d1'))
    await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
    const { days } = await openSheet()
    fireEvent.click(within(days).getByRole('button', { name: /Thursday 8/ }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Added to Thursday this week.'))

    // The Plan shows the swapped plan, not changed since its push, and no
    // longer says the earlier plan's Supply run was made.
    await act(() => router.navigate('/plan'))
    expect(await screen.findByText('Changes not pushed to Google Calendar')).toBeInTheDocument()
    expect(screen.getByText('Korean beef bowls')).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByText("Made this week's Supply run from the plan.")).not.toBeInTheDocument()
    expect(sent(fetchMock, 'POST')).toEqual([`${THIS_WEEK}/push-calendar undefined`])
    expect(sent(fetchMock, 'PUT')).toEqual([`${THURSDAY} {"dinner_id":"d1"}`])
  })

  it('refuses a swap while the week is being pushed to Calendar', async () => {
    const push = deferred()
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('2026-10-05', THIS_NAMES)),
      [`POST ${THIS_WEEK}/push-calendar`]: () => push.response,
      'GET /api/v1/shopping/2026-10-05': () => errorResponse(404, 'not_found'),
    })
    const router = await renderPage(<PlanPage />)
    await act(() => router.navigate('/plan'))
    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    await waitFor(() => expect(sent(fetchMock, 'POST')).toHaveLength(1))

    await act(() => router.navigate('/dinner/d1'))
    await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
    const { sheet, days } = await openSheet()
    expect(within(sheet).getByText('The plan for this week is being pushed to Calendar.')).toBeInTheDocument()
    expect(within(days).queryAllByRole('button')).toHaveLength(0)
    expect(sent(fetchMock, 'PUT')).toEqual([])
  })
})

describe('the favorite heart', () => {
  it('fills at once, keeps the recipe and takes the server answer', async () => {
    const favorite = deferred()
    const fetchMock = stubServer({ [`POST ${FAVORITE}`]: () => favorite.response })
    await renderPage()
    const heart = screen.getByRole('button', { name: 'Favorite' })
    expect(heart).toHaveAttribute('aria-pressed', 'false')

    fireEvent.click(heart)
    await waitFor(() => expect(heart).toHaveAttribute('aria-pressed', 'true'))
    expect(sent(fetchMock, 'POST')).toEqual([`${FAVORITE} undefined`])

    await act(async () => favorite.send(jsonResponse(200, listed(true))))
    await waitFor(() => expect(sent(fetchMock, 'POST')).toHaveLength(1))
    expect(heart).toHaveAttribute('aria-pressed', 'true')
    // The list-shaped answer did not replace the detail: the recipe stays.
    expect(screen.getByText('Ground beef')).toBeInTheDocument()
    expect(screen.getByText('Brown the beef with the sauce.')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('empties again with the error when the server refuses, and retries', async () => {
    const refused = deferred()
    let refuse = true
    const fetchMock = stubServer({
      [`GET ${DINNER}`]: () => jsonResponse(200, detail({ is_favorite: true })),
      [`POST ${FAVORITE}`]: () => (refuse ? refused.response : jsonResponse(200, listed(false))),
    })
    await renderPage()
    const heart = screen.getByRole('button', { name: 'Favorite' })
    expect(heart).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(heart)
    await waitFor(() => expect(heart).toHaveAttribute('aria-pressed', 'false'))
    await act(async () => refused.send(errorResponse(500, 'internal')))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't unfavorite Korean beef bowls. internal message")
    expect(heart).toHaveAttribute('aria-pressed', 'true')

    refuse = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(heart).toHaveAttribute('aria-pressed', 'false'))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    expect(sent(fetchMock, 'POST')).toHaveLength(2)
  })
})
