import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { AppRoutes } from '../App.tsx'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { cacheFallbackHeader } from '../sw/routes.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { HOME_PATH } from './api.ts'

const WEEK = '2026-10-05'
const PLAN = `/api/v1/plans/${WEEK}`
const WEDNESDAY = `${PLAN}/days/2026-10-07`
const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const NAMES = ['Beef tacos', 'Spaghetti bolognese', 'Crock pot tikka masala', 'Chicken stir-fry', 'Pizza night', 'Korean beef bowls', 'Sheet-pan salmon']

// Wednesday, Oct 7, noon in New York: tonight on the board is the week's
// Wednesday, and the plan opens on this week (before Friday's plan draft).
const NOW = new Date('2026-10-07T16:00:00Z')

const TONIGHT = {
  id: 'd2',
  name: 'Crock pot tikka masala',
  status: 'quest_log',
  photo: null,
  origin: 'side_quest',
  minutes_total: 240,
  meal_source: 'cook',
}

const FISH = {
  id: 'r1',
  name: 'Fish tacos',
  status: 'regular',
  photo: null,
  origin: 'regular',
  minutes_total: 35,
  meal_source: 'cook',
}

type Evening = 'delivery' | 'dining_out' | null
type Tonight = typeof TONIGHT | typeof FISH | null

// homeOf is GET /api/v1/home for this Wednesday: a cooked dinner, a Delivery
// or Dining-out evening, or a cleared night.
function homeOf(tonight: Tonight, evening: Evening) {
  return {
    dateLabel: 'Wednesday, Oct 7',
    timezone: 'America/New_York',
    tonight,
    manualTonight: null,
    evening,
    sideQuestCount: 0,
    shoppingList: { weekStart: WEEK, label: 'Week 41 · Oct 5 – 11', ready: false, sentToChatAt: null },
    recentUpdates: { unread: false },
  }
}

function planBody() {
  return {
    id: 'plan-1',
    week_start: WEEK,
    status: 'draft',
    ai_note: null,
    calendar_synced_at: null,
    days: DAYS.map((date, i) => ({
      date,
      dinner: {
        id: `d${i}`,
        name: NAMES[i],
        status: i === 2 || i === 5 ? 'quest_log' : 'regular',
        photo: null,
        minutes: i === 2 ? 240 : 30,
        method: i === 2 ? 'crockpot' : 'stovetop',
        origin: i === 2 || i === 5 ? 'side_quest' : 'regular',
        meal_source: 'cook',
      },
      is_new: i === 2 || i === 5,
      start_by: i === 2 ? '2026-10-07T13:30:00-04:00' : null,
      meal_source: 'cook' as const,
    })),
  }
}

type PlanBody = ReturnType<typeof planBody>

function replaceWednesday(day: PlanBody['days'][number]): PlanBody {
  const body = planBody()
  body.days[2] = day
  return body
}

function eveningPlan(source: Exclude<Evening, null>): PlanBody {
  return replaceWednesday({
    date: DAYS[2]!,
    dinner: null,
    manual_label: null,
    meal_source: source,
    is_new: false,
    start_by: null,
  } as unknown as PlanBody['days'][number])
}

function fishPlan(): PlanBody {
  const body = planBody()
  const day = body.days[2]!
  body.days[2] = {
    ...day,
    dinner: { ...day.dinner, id: 'r1', name: 'Fish tacos', status: 'regular', minutes: 35, method: 'stovetop', origin: 'regular' },
    is_new: false,
    start_by: null,
  }
  return body
}

function clearedPlan(): PlanBody {
  return replaceWednesday({
    date: DAYS[2]!,
    dinner: null,
    manual_label: null,
    meal_source: 'cook',
    is_new: false,
    start_by: null,
  } as unknown as PlanBody['days'][number])
}

function dinner(id: string, name: string, difficulty: string | null, minutes: number | null) {
  return {
    id,
    name,
    method: 'stovetop',
    minutes_total: minutes,
    difficulty,
    is_favorite: false,
    photo_url: null,
    grounding_status: 'grounded',
    grounding_error: null,
    photo_backfill_eligible: false,
    meal_source: 'cook',
  }
}

type Responder = () => Response | Promise<Response>

// stubServer answers "METHOD path". A week's Supply run that was not stubbed
// is absent (404), as a week with no list is.
function stubServer(responders: Record<string, Responder>) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    'GET /api/v1/rotation-reviews': () => jsonResponse(200, { reviews: [] }),
    'GET /api/v1/dinners?status=quest_log': () => jsonResponse(200, { dinners: [dinner('q1', 'Lemon orzo', 'easy', 25)] }),
    'GET /api/v1/dinners?status=regular': () =>
      jsonResponse(200, { dinners: [dinner('d0', 'Beef tacos', 'easy', 30), dinner('r1', 'Fish tacos', 'medium', 35)] }),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key)
      ? all[key]
      : /^GET \/api\/v1\/shopping\/\d{4}-\d{2}-\d{2}$/.test(key)
        ? () => errorResponse(404, 'not_found')
        : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond())
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function homeReads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === HOME_PATH).length
}

function puts(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([input, init]) => `${String(input)} ${String(init?.body)}`)
}

function go(label: 'Home' | 'Plan') {
  fireEvent.click(within(screen.getByRole('navigation', { name: 'Primary' })).getByRole('link', { name: label }))
}

// The clock stays at noon, inside Home's 30-second cache and far from the
// midnight refresh, so a return to Home shows a new Tonight only when the
// board edit invalidated it.
beforeEach(() => {
  mockViewport(390, { reducedMotion: true })
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

describe('Home after a board evening edit', () => {
  it("refetches Tonight after the board sets Delivery or Dining out, replaces it, or clears it", async () => {
    let plan = planBody()
    let home = homeOf(TONIGHT, null)
    let nextPlan = plan
    let nextHome = home
    const fetchMock = stubServer({
      [`GET ${HOME_PATH}`]: () => jsonResponse(200, home),
      [`GET ${PLAN}`]: () => jsonResponse(200, plan),
      [`PUT ${WEDNESDAY}`]: () => {
        plan = nextPlan
        home = nextHome
        return jsonResponse(200, plan)
      },
    })
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/">
            <AppRoutes />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Crock pot tikka masala' })).toBeInTheDocument()
    expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(1)

    go('Plan')
    expect(await screen.findByRole('heading', { name: 'Oct 5 – 11' })).toBeInTheDocument()
    await screen.findByText('Crock pot tikka masala')

    async function edit(choose: () => void, body: string) {
      choose()
      await waitFor(() => expect(puts(fetchMock).at(-1)).toBe(`${WEDNESDAY} ${body}`))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Evening for Wednesday' })).toBeEnabled())
    }

    function chooseEvening(name: 'Delivery' | 'Dining out' | 'Clear this night') {
      fireEvent.click(screen.getByRole('button', { name: 'Evening for Wednesday' }))
      const menu = screen.getByRole('menu', { name: 'Evening for Wednesday' })
      const item = name === 'Clear this night' ? 'menuitem' : 'menuitemradio'
      fireEvent.click(within(menu).getByRole(item, { name }))
    }

    nextPlan = eveningPlan('delivery')
    nextHome = homeOf(null, 'delivery')
    await edit(() => chooseEvening('Delivery'), '{"meal_source":"delivery"}')
    await backToHome('Delivery', false)
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()

    go('Plan')
    expect(await screen.findByText('Delivery')).toBeInTheDocument()
    nextPlan = eveningPlan('dining_out')
    nextHome = homeOf(null, 'dining_out')
    await edit(() => chooseEvening('Dining out'), '{"meal_source":"dining_out"}')
    await backToHome('Dining out', false)
    expect(screen.queryByRole('heading', { name: 'Delivery' })).not.toBeInTheDocument()

    go('Plan')
    expect(await screen.findByText('Dining out')).toBeInTheDocument()
    fireEvent.click(screen.getByText('WED').closest('li')!.querySelector('button')!)
    const sheet = await screen.findByRole('dialog', { name: 'Swap Wednesday' })
    fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
    nextPlan = fishPlan()
    nextHome = homeOf(FISH, null)
    await edit(() => fireEvent.click(within(sheet).getByRole('button', { name: /Fish tacos/ })), '{"dinner_id":"r1"}')
    await backToHome('Fish tacos', true)
    expect(screen.queryByRole('heading', { name: 'Dining out' })).not.toBeInTheDocument()

    go('Plan')
    expect(await screen.findByText('Fish tacos')).toBeInTheDocument()
    nextPlan = clearedPlan()
    nextHome = homeOf(null, null)
    await edit(() => chooseEvening('Clear this night'), '{"clear":true}')
    const reads = homeReads(fetchMock)
    go('Home')
    expect(await screen.findByRole('heading', { name: 'No quest planned tonight' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Fish tacos' })).not.toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 1)

    async function backToHome(name: string, recipe: boolean) {
      const reads = homeReads(fetchMock)
      go('Home')
      await screen.findByText('Wednesday, Oct 7')
      await waitFor(() => {
        const card = screen.getByRole('region', { name: 'Tonight' })
        expect(within(card).getByRole('heading', { name })).toBeInTheDocument()
        if (recipe) expect(within(card).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
        else expect(within(card).queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
      })
      expect(homeReads(fetchMock)).toBe(reads + 1)
    }
  })

  // The Home read after a successful swap is held open. From the return to
  // Home until that read resolves, the dinner this edit replaced and its
  // Recipe button stay off the page, and Home shows its loading skeleton.
  it('never shows the previous dinner or its Recipe button while the fresh Home read is pending', async () => {
    let plan = planBody()
    let home = homeOf(TONIGHT, null)
    let releaseHeldHome: (() => void) | undefined
    let holdHomeRead = false
    const fetchMock = stubServer({
      [`GET ${HOME_PATH}`]: () => {
        if (!holdHomeRead) return jsonResponse(200, home)
        const response = jsonResponse(200, home)
        return new Promise<Response>((resolve) => {
          releaseHeldHome = () => resolve(response)
        })
      },
      [`GET ${PLAN}`]: () => jsonResponse(200, plan),
      [`PUT ${WEDNESDAY}`]: () => {
        plan = fishPlan()
        home = homeOf(FISH, null)
        return jsonResponse(200, plan)
      },
    })
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/">
            <AppRoutes />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Crock pot tikka masala' })).toBeInTheDocument()
    expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(1)

    go('Plan')
    expect(await screen.findByRole('heading', { name: 'Oct 5 – 11' })).toBeInTheDocument()
    await screen.findByText('Crock pot tikka masala')
    fireEvent.click(screen.getByText('WED · NEW').closest('li')!.querySelector('button')!)
    const sheet = await screen.findByRole('dialog', { name: 'Swap Wednesday' })
    fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
    fireEvent.click(within(sheet).getByRole('button', { name: /Fish tacos/ }))
    await waitFor(() => expect(puts(fetchMock).at(-1)).toBe(`${WEDNESDAY} {"dinner_id":"r1"}`))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Evening for Wednesday' })).toBeEnabled())
    expect(screen.getByText('Fish tacos')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()

    const seen: string[] = []
    const note = () => {
      if (screen.queryByRole('heading', { name: 'Crock pot tikka masala' })) seen.push('Crock pot tikka masala')
      if (screen.queryByRole('button', { name: 'Recipe' })) seen.push('Recipe')
    }
    const observer = new MutationObserver(note)
    observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    const reads = homeReads(fetchMock)
    holdHomeRead = true
    try {
      go('Home')
      note()
      expectNoStaleTonight()
      expect(homeReads(fetchMock)).toBe(reads + 1)
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50))
      })
      note()
      expect(seen).toEqual([])
      expectNoStaleTonight()
      expect(homeReads(fetchMock)).toBe(reads + 1)
    } finally {
      observer.disconnect()
    }

    releaseHeldHome!()
    const card = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(card).getByRole('heading', { name: 'Fish tacos' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tonight')).not.toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 1)
  })

  // Home can mount again while the board edit is still pending, so it paints
  // the cached Tonight. When that edit then succeeds, the previous dinner and
  // its Recipe button leave, including while the fresh Home read is held, and
  // the dinner the edit chose appears once that read resolves.
  it('clears a mounted Home Tonight when the board edit succeeds after returning while that edit is still pending', async () => {
    let plan = planBody()
    let home = homeOf(TONIGHT, null)
    let releasePut: (() => void) | undefined
    let releaseHeldHome: (() => void) | undefined
    let holdHomeRead = false
    const fetchMock = stubServer({
      [`GET ${HOME_PATH}`]: () => {
        if (!holdHomeRead) return jsonResponse(200, home)
        return new Promise<Response>((resolve) => {
          releaseHeldHome = () => resolve(jsonResponse(200, home))
        })
      },
      [`GET ${PLAN}`]: () => jsonResponse(200, plan),
      [`PUT ${WEDNESDAY}`]: () =>
        new Promise<Response>((resolve) => {
          releasePut = () => {
            plan = fishPlan()
            home = homeOf(FISH, null)
            resolve(jsonResponse(200, plan))
          }
        }),
    })
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/">
            <AppRoutes />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    const first = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(first).getByRole('heading', { name: 'Crock pot tikka masala' })).toBeInTheDocument()
    expect(within(first).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(1)

    go('Plan')
    expect(await screen.findByRole('heading', { name: 'Oct 5 – 11' })).toBeInTheDocument()
    await screen.findByText('Crock pot tikka masala')
    fireEvent.click(screen.getByText('WED · NEW').closest('li')!.querySelector('button')!)
    const sheet = await screen.findByRole('dialog', { name: 'Swap Wednesday' })
    fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
    fireEvent.click(within(sheet).getByRole('button', { name: /Fish tacos/ }))
    await waitFor(() => expect(puts(fetchMock).at(-1)).toBe(`${WEDNESDAY} {"dinner_id":"r1"}`))
    expect(screen.getByRole('button', { name: 'Evening for Wednesday' })).toBeDisabled()

    go('Home')
    const cached = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(cached).getByRole('heading', { name: 'Crock pot tikka masala' })).toBeInTheDocument()
    expect(within(cached).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(1)

    const reads = homeReads(fetchMock)
    holdHomeRead = true
    releasePut!()
    await waitFor(() => {
      expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
    })
    expect(screen.getByTestId('skeleton-tonight')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Fish tacos' })).not.toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 1)
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50))
    })
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
    expect(screen.getByTestId('skeleton-tonight')).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 1)

    releaseHeldHome!()
    const card = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(card).getByRole('heading', { name: 'Fish tacos' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tonight')).not.toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 1)
  })

  // The PUT succeeded, but the Home read it starts is answered from the
  // service worker's cache (the previous Tonight, marked as a fallback).
  // That copy is not server-current, so it must not become Tonight; the
  // card stays in its non-stale state until a live read lands.
  it('does not render the previous dinner when the Home read after a board edit is a service-worker cache fallback', async () => {
    let plan = planBody()
    let home = homeOf(TONIGHT, null)
    const previous = homeOf(TONIGHT, null)
    let fallback = false
    const fetchMock = stubServer({
      [`GET ${HOME_PATH}`]: () => {
        if (!fallback) return jsonResponse(200, home)
        const response = jsonResponse(200, previous)
        response.headers.set(cacheFallbackHeader, '1')
        return response
      },
      [`GET ${PLAN}`]: () => jsonResponse(200, plan),
      [`PUT ${WEDNESDAY}`]: () => {
        plan = fishPlan()
        home = homeOf(FISH, null)
        return jsonResponse(200, plan)
      },
    })
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/">
            <AppRoutes />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Crock pot tikka masala' })).toBeInTheDocument()
    expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(1)
    expect(fetchMock.mock.calls.find(([input]) => String(input) === HOME_PATH)?.[1]?.cache).toBeUndefined()

    go('Plan')
    expect(await screen.findByRole('heading', { name: 'Oct 5 – 11' })).toBeInTheDocument()
    await screen.findByText('Crock pot tikka masala')
    fireEvent.click(screen.getByText('WED · NEW').closest('li')!.querySelector('button')!)
    const sheet = await screen.findByRole('dialog', { name: 'Swap Wednesday' })
    fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
    fireEvent.click(within(sheet).getByRole('button', { name: /Fish tacos/ }))
    await waitFor(() => expect(puts(fetchMock).at(-1)).toBe(`${WEDNESDAY} {"dinner_id":"r1"}`))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Evening for Wednesday' })).toBeEnabled())
    expect(screen.getByText('Fish tacos')).toBeInTheDocument()

    const seen: string[] = []
    const note = () => {
      if (screen.queryByRole('heading', { name: 'Crock pot tikka masala' })) seen.push('Crock pot tikka masala')
      if (screen.queryByRole('button', { name: 'Recipe' })) seen.push('Recipe')
    }
    const observer = new MutationObserver(note)
    observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    const reads = homeReads(fetchMock)
    fallback = true
    try {
      go('Home')
      note()
      await waitFor(() => {
        expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
        expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
        expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
        expect(screen.getByRole('alert')).toHaveTextContent("Couldn't load tonight's quest.")
        expect(screen.getByRole('alert')).toHaveTextContent("answered from the service worker's cache, not the server")
      })
      expect(seen).toEqual([])
      expect(homeReads(fetchMock)).toBe(reads + 1)
      const postEdit = fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === HOME_PATH).at(-1)
      expect(postEdit?.[1]?.cache).toBe('no-store')
    } finally {
      observer.disconnect()
    }

    fallback = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    const card = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(card).getByRole('heading', { name: 'Fish tacos' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(homeReads(fetchMock)).toBe(reads + 2)
    const live = fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === HOME_PATH).at(-1)
    expect(live?.[1]?.cache).toBe('no-store')
  })
})

function expectNoStaleTonight() {
  expect(screen.queryByRole('heading', { name: 'Crock pot tikka masala' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Fish tacos' })).not.toBeInTheDocument()
  expect(screen.getByTestId('skeleton-tonight')).toBeInTheDocument()
}
