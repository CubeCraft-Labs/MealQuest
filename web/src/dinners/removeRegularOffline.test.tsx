// A Regular removed while reads answered before still have it — saved by the
// service worker, or still in flight: the pages read through the worker's
// real API strategy (src/test/serviceWorker.ts), so what they show offline is
// what the worker kept. jsdom has no service worker, so the strategy runs
// directly against an in-memory cache.
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Link, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { dinnerDetailKey, RemovalCheck } from './api.ts'
import { ARCHIVED, archivedKey, REMOVING, resetArchived } from './archived.ts'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'
import { HOME_KEY } from '../home/api.ts'
import { HomePage } from '../home/HomePage.tsx'
import { planKey } from '../plan/api.ts'
import { PlanPage } from '../plan/PlanPage.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { OfflinePill } from '../pwa/OfflinePill.tsx'
import { RegularsSettingsPage } from '../settings/RegularsSettingsPage.tsx'
import { supplyRunKey, SupplyRunSync } from '../shopping/api.ts'
import { SAVED_KEY } from '../shopping/offline.ts'
import { SupplyRunPage } from '../shopping/SupplyRunPage.tsx'
import { HOME, HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { origin, stubFetchThroughWorker, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'

const WEEK = '2026-10-05'
const PLAN_SCREEN = '/plan'
const RUN_SCREEN = `/plan/shopping?week=${WEEK}`
const REGULARS_SCREEN = '/settings/regulars'

const TACOS = dinnerBody('d1', 'Beef tacos')
const ROAST = dinnerBody('d4', 'Pot roast')

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const NAMES = ['Pot roast', 'Chili', 'Beef tacos', 'Chicken stir-fry', 'Pizza night', 'Korean beef bowls', 'Sheet-pan salmon']
const IDS = ['d4', 'd5', 'd1', 'd6', 'd7', 'd8', 'd9']

// The week's plan, Home and Supply run as the server sends them while Beef
// tacos (d1), tonight's dinner, is a Regular; planWith, homeWith and runWith
// send it with Beef tacos' status as status.
const plan = {
  id: 'plan-1',
  week_start: WEEK,
  week_label: 'Week 41 · Oct 5 – 11',
  status: 'synced',
  ai_note: null,
  calendar_synced_at: '2026-10-04T15:00:00Z',
  days: DAYS.map((date, i) => ({
    date,
    dinner: { id: IDS[i], name: NAMES[i], status: 'regular', photo: null, minutes: 30, method: 'stovetop', origin: 'regular' },
    is_new: false,
    start_by: null,
  })),
  updated_at: '2026-10-04T15:00:00Z',
}
const planWith = (status: string) => ({
  ...plan,
  days: plan.days.map((day) => (day.dinner.id === 'd1' ? { ...day, dinner: { ...day.dinner, status } } : day)),
})
const home = { ...HOME, tonight: { ...HOME.tonight, id: 'd1', name: 'Beef tacos', status: 'regular' } }
const run = {
  weekStart: WEEK,
  label: 'Week 41 · Oct 5 – 11',
  title: null,
  status: 'current',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 2,
  total_count: 2,
  groups: [
    { dinner: { id: 'd4', name: 'Pot roast', status: 'regular' }, date: '2026-10-05', day: 'Monday', items: [{ id: 'i2', name: 'Chuck roast', qty: '3 lb', needed: true }] },
    { dinner: { id: 'd1', name: 'Beef tacos', status: 'regular' }, date: '2026-10-07', day: 'Wednesday', items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }] },
  ],
}
const homeWith = (status: string) => ({ ...home, tonight: { ...home.tonight, status } })
const runWith = (status: string) => ({
  ...run,
  groups: run.groups.map((group) => (group.dinner.id === 'd1' ? { ...group, dinner: { ...group.dinner, status } } : group)),
})

// Beef tacos' detail, with its recipe, while it is a Regular.
const TACOS_RECIPE = {
  ...TACOS,
  photo_urls: null,
  ingredients: [{ name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' }],
  steps: ['Brown the beef.'],
  source: { site_name: 'Example Kitchen' },
  why_picked: null,
}

let cache: FakeCache
let reachable: boolean
// networkReads is every "METHOD path?search" the network was asked for,
// reached or not.
let networkReads: string[]
// archived says the server has archived Beef tacos.
let archived: boolean
// While holding, the server takes each read of HELD as it comes, answering
// it as things are then, and keeps the answer back until release is called;
// held lists each release.
let holding: boolean
let held: Array<() => void>
const HELD = [`/api/v1/plans/${WEEK}`, '/api/v1/home', `/api/v1/shopping/${WEEK}`, '/api/v1/dinners/d1']

// server answers "METHOD path?search" while it can be reached.
const server: Record<string, () => Response> = {
  'GET /api/v1/me': () => jsonResponse(200, ME),
  'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
  'GET /api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: archived ? [ROAST] : [TACOS, ROAST] }),
  'GET /api/v1/dinners?status=quest_log': () => jsonResponse(200, { dinners: [] }),
  'GET /api/v1/dinners/d1': () => (archived ? errorResponse(404, 'not_found') : jsonResponse(200, TACOS_RECIPE)),
  [`GET /api/v1/plans/${WEEK}`]: () => jsonResponse(200, planWith(archived ? 'archived' : 'regular')),
  'GET /api/v1/plans/2026-10-12': () => errorResponse(404, 'not_found'),
  'GET /api/v1/home': () => jsonResponse(200, homeWith(archived ? 'archived' : 'regular')),
  'GET /api/v1/rotation-reviews': () => jsonResponse(200, { reviews: [] }),
  [`GET /api/v1/shopping/${WEEK}`]: () => jsonResponse(200, runWith(archived ? 'archived' : 'regular')),
}

// archiveTacos is the server archiving Beef tacos, which every test starts
// with as its answer to the removal.
function archiveTacos() {
  archived = true
  return jsonResponse(200, { ...TACOS, status: 'archived' })
}

beforeEach(() => {
  server['DELETE /api/v1/dinners/d1'] = archiveTacos
  cache = stubWorkerCaches()
  reachable = true
  networkReads = []
  archived = false
  holding = false
  held = []
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
  stubFetchThroughWorker(async (request) => {
    const url = new URL(request.url)
    const key = `${request.method} ${url.pathname}${url.search}`
    networkReads.push(key)
    if (!reachable) throw new TypeError('Failed to fetch')
    const respond = Object.hasOwn(server, key) ? server[key] : undefined
    if (respond === undefined) throw new Error(`unexpected network fetch: ${key}`)
    const response = respond()
    if (holding && request.method === 'GET' && HELD.includes(url.pathname)) await new Promise<void>((release) => held.push(release))
    return response
  })
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

// renderApp loads the app at path as a fresh page load would — a new API
// client, network status, query cache and record of archived dinners read
// from this device — with the Plan, Home, Supply run, Regulars and dinner
// screens, SupplyRunSync, RemovalCheck and the offline indicator, as App
// does; each screen
// links to the others.
async function renderApp(path: string): Promise<{ queryClient: QueryClient; unmount: () => void }> {
  resetArchived()
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  if (reachable) await api.loadMe()
  const queryClient = createQueryClient()
  const { unmount } = render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <SupplyRunSync />
        <RemovalCheck />
        <InDataRouter path={path}>
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="/plan" element={<PlanPage />} />
            <Route path="/plan/shopping" element={<SupplyRunPage />} />
            <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
            <Route path="/dinner/:id" element={<DinnerDetailPage />} />
          </Routes>
          <nav>
            <Link to="/">To Home</Link>
            <Link to={PLAN_SCREEN}>To Plan</Link>
            <Link to={RUN_SCREEN}>To Supply run</Link>
            <Link to={REGULARS_SCREEN}>To Regulars</Link>
            {/* Not a recipe link (recipeLinks): its search sets it apart. */}
            <Link to="/dinner/d1?typed=1">To Beef tacos</Link>
          </nav>
        </InDataRouter>
        <OfflinePill />
      </QueryClientProvider>
    </ApiProvider>,
  )
  return { queryClient, unmount }
}

// recipeLinks lists every link on the page to dinner id's recipe.
function recipeLinks(id: string) {
  return screen.queryAllByRole('link').filter((link) => link.getAttribute('href') === `/dinner/${id}`)
}

function go(name: string) {
  fireEvent.click(screen.getByRole('link', { name }))
}

// savedRegular lists the reads the worker saved that have dinner id a
// Regular, as the server sent it before it was archived.
async function savedRegular(id: string) {
  const holding: string[] = []
  for (const [url, response] of cache.entries) {
    const text = await response.clone().text()
    if (text.includes(`"id":${JSON.stringify(id)}`) && !text.includes('"archived"')) holding.push(url.slice(origin.length))
  }
  return holding.sort()
}

// read waits until path has been read again and every read of the page is
// done, so what shows is what the read left.
async function read(queryClient: QueryClient, path: string) {
  await waitFor(() => expect(networkReads).toContain(`GET ${path}`))
  await waitFor(() => expect(queryClient.isFetching()).toBe(0))
}

// readAgain reads the plan, Home, Supply run and Beef tacos' detail again, as
// when the app comes back to the front, and resolves once each is answered.
function readAgain(queryClient: QueryClient) {
  return Promise.all(
    [planKey(WEEK), HOME_KEY, supplyRunKey(WEEK), dinnerDetailKey('d1')].map((queryKey) =>
      queryClient.refetchQueries({ queryKey, exact: true, type: 'all' }),
    ),
  )
}

function savedStatus(id: string) {
  const saved = JSON.parse(localStorage.getItem(SAVED_KEY)!) as { runs: Record<string, typeof run> }
  return saved.runs[WEEK]!.groups.find((group) => group.dinner.id === id)!.dinner.status
}

// readOnline reads, online, the plan, Home, Supply run and Beef tacos'
// detail, each linking to or showing its recipe, so the worker saves each,
// and ends on the Regulars.
async function readOnline() {
  await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
  go('To Home')
  await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
  go('To Supply run')
  await waitFor(() => expect(within(screen.getByRole('region', { name: 'Beef tacos' })).getByRole('link')).toHaveAttribute('href', '/dinner/d1'))
  await waitFor(() => expect(savedStatus('d1')).toBe('regular'))
  go('To Beef tacos')
  expect(await screen.findByText('Brown the beef.')).toBeInTheDocument()
  go('To Regulars')
  await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
}

function removeTacos() {
  fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
  fireEvent.click(screen.getByRole('button', { name: 'Remove from Regulars' }))
  fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
}

// expectNoRecipeLink checks, screen by screen, that nothing links to Beef
// tacos' recipe or shows its detail: the plan and Home show it archived, the
// Supply run's card is plain with its items still toggling, the Regulars no
// longer have it and its detail is not found. Pot roast still links.
async function expectNoRecipeLink() {
  go('To Plan')
  await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })
  await waitFor(() => expect(recipeLinks('d4')).toHaveLength(1))
  expect(screen.getByText('Beef tacos')).toBeInTheDocument()
  expect(recipeLinks('d1')).toEqual([])

  go('To Home')
  const tonight = await screen.findByRole('region', { name: 'Tonight' })
  expect(within(tonight).getByRole('heading', { name: 'Beef tacos' })).toBeInTheDocument()
  expect(recipeLinks('d1')).toEqual([])

  go('To Supply run')
  const card = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
  expect(within(card).queryByRole('link')).not.toBeInTheDocument()
  expect(within(card).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
  expect(within(screen.getByRole('region', { name: 'Pot roast' })).getByRole('link')).toHaveAttribute('href', '/dinner/d4')

  go('To Regulars')
  await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
  expect(screen.queryByRole('button', { name: 'Edit Beef tacos' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Edit Pot roast' })).toBeInTheDocument()

  go('To Beef tacos')
  expect(await screen.findByText('Dinner not found')).toBeInTheDocument()
  expect(screen.queryByText('Brown the beef.')).not.toBeInTheDocument()
}

describe('a removed Regular read offline from what the worker saved before', () => {
  it('links to its recipe on no plan, Home, Supply run, Regulars or detail', async () => {
    // From tablet up the plan manager's day cards link to their recipes.
    mockViewport(768)
    const online = await renderApp(PLAN_SCREEN)
    await readOnline()
    expect(await savedRegular('d1')).toEqual([
      '/api/v1/dinners/d1',
      '/api/v1/dinners?status=regular',
      '/api/v1/home',
      `/api/v1/plans/${WEEK}`,
      `/api/v1/shopping/${WEEK}`,
    ])

    // Beef tacos leaves the Regulars, and is recorded archived on this
    // device; the Supply run saved here is saved again with it archived.
    removeTacos()
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED)
    expect(savedStatus('d1')).toBe('archived')

    // The server can no longer be reached and everything is read again. The
    // worker answers with what it saved before, Beef tacos a Regular in
    // each, yet the record leaves no screen linking to its recipe.
    reachable = false
    networkReads = []
    await readAgain(online.queryClient)
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`, 'GET /api/v1/dinners/d1']),
    )
    expect(await savedRegular('d1')).toContain(`/api/v1/plans/${WEEK}`)
    await expectNoRecipeLink()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    online.unmount()

    // A new page offline opens the Supply run saved on this device; the
    // record saved here leaves it no recipe link, its items still toggling.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    const reopened = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(reopened).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
    expect(within(screen.getByRole('region', { name: 'Pot roast' })).getByRole('link')).toHaveAttribute('href', '/dinner/d4')
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
  })
})

describe('a read answered before a Regular is removed that arrives after', () => {
  it('links to its recipe on no screen, online or later from what the worker saved of it', async () => {
    mockViewport(768)
    const online = await renderApp(PLAN_SCREEN)
    await readOnline()

    // The plan, Home, Supply run and Beef tacos' detail are read again; the
    // server answers each while Beef tacos is a Regular and holds the
    // answers back.
    holding = true
    const late = readAgain(online.queryClient)
    await waitFor(() => expect(held).toHaveLength(HELD.length))

    // Beef tacos leaves the Regulars while those reads are outstanding.
    removeTacos()
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(archived).toBe(true)

    // The held answers arrive now, and the worker saves each.
    holding = false
    held.splice(0).forEach((release) => release())
    await late
    await waitFor(() => expect(online.queryClient.isFetching()).toBe(0))
    expect(await savedRegular('d1')).toEqual([
      '/api/v1/dinners/d1',
      '/api/v1/home',
      `/api/v1/plans/${WEEK}`,
      `/api/v1/shopping/${WEEK}`,
    ])
    // The late Supply run is saved on this device with Beef tacos archived.
    expect(savedStatus('d1')).toBe('archived')

    // Online, no screen links to its recipe.
    await expectNoRecipeLink()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

    // The server can no longer be reached: the worker answers every read
    // again with the late answers it saved, and still no screen links to
    // Beef tacos' recipe.
    reachable = false
    networkReads = []
    await readAgain(online.queryClient)
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`, 'GET /api/v1/dinners/d1']),
    )
    await expectNoRecipeLink()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    online.unmount()

    // Nor does a new page offline, from the record saved on this device.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    const reopened = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(reopened).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
  })
})

describe('a removal no answer settled', () => {
  it('links to its recipe nowhere, even after a reload, until the server says it archived it', async () => {
    mockViewport(768)
    const online = await renderApp(PLAN_SCREEN)
    await readOnline()

    // The server archives Beef tacos, but its answer never arrives, and it
    // cannot be reached when asked at once.
    server['DELETE /api/v1/dinners/d1'] = () => {
      archived = true
      reachable = false
      throw new TypeError('Failed to fetch')
    }
    removeTacos()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not remove Beef tacos: Failed to fetch. The server may have removed it, so this device hides it until the server can say whether it did.',
    )
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    expect(savedStatus('d1')).toBe('regular')

    // Offline, every read is the worker's from before, yet nothing links.
    networkReads = []
    await readAgain(online.queryClient)
    await expectNoRecipeLink()
    online.unmount()

    // A new page offline opens the saved Supply run: still no link.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(recipeLinks('d1')).toEqual([])
    runPage.unmount()

    // A new page that can reach the server asks it, and marks Beef tacos
    // archived, the saved Supply run with it.
    reachable = true
    networkReads = []
    await renderApp(RUN_SCREEN)
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED))
    expect(networkReads).toContain('GET /api/v1/dinners/d1')
    await waitFor(() => expect(savedStatus('d1')).toBe('archived'))
    expect(recipeLinks('d1')).toEqual([])
  })

  it('shows the Regular again, with its recipe, once the server can say it did not archive it', async () => {
    mockViewport(768)
    const online = await renderApp(PLAN_SCREEN)
    await readOnline()

    // The removal never reaches the server.
    reachable = false
    removeTacos()
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not remove Beef tacos: Failed to fetch. The server may have removed it')
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(archived).toBe(false)
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    expect(screen.queryByRole('button', { name: 'Edit Beef tacos' })).not.toBeInTheDocument()

    // The server can be reached again: asked, it still has Beef tacos, so
    // the mark goes and Beef tacos is a Regular with its recipe again.
    reachable = true
    await readAgain(online.queryClient)
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBeNull())
    expect(await screen.findByRole('button', { name: 'Edit Beef tacos' })).toBeInTheDocument()
    go('To Plan')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
  })
})
