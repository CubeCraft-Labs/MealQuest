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
import { dinnerDetailKey, REGULARS_KEY, RemovalCheck } from './api.ts'
import { ARCHIVED, archivedKey, removalLock, REMOVING, resetArchived } from './archived.ts'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'
import { QUEST_LOG_KEY } from '../discover/api.ts'
import { DiscoverPage } from '../discover/DiscoverPage.tsx'
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
import { deviceLocks } from '../test/locks.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { origin, stubFetchThroughWorker, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'

const WEEK = '2026-10-05'
const PLAN_SCREEN = '/plan'
const RUN_SCREEN = `/plan/shopping?week=${WEEK}`
const REGULARS_SCREEN = '/settings/regulars'
const DISCOVER_SCREEN = '/discover'

const TACOS = dinnerBody('d1', 'Beef tacos')
const ROAST = dinnerBody('d4', 'Pot roast')
// The Quest log: Shrimp gumbo, which another device archives in some tests
// (questArchived), and Pad thai, which the server always has.
const GUMBO = dinnerBody('q1', 'Shrimp gumbo', { status: 'quest_log', origin: 'side_quest' })
const PAD_THAI = dinnerBody('q2', 'Pad thai', { status: 'quest_log', origin: 'side_quest' })

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
    dinner: { id: IDS[i], name: NAMES[i], status: 'regular', photo: null, minutes: 30, method: 'stovetop', origin: 'regular', meal_source: 'cook' },
    is_new: false,
    start_by: null,
    meal_source: 'cook',
  })),
  updated_at: '2026-10-04T15:00:00Z',
}
const planWith = (status: string) => ({
  ...plan,
  days: plan.days.map((day) => (day.dinner?.id === 'd1' ? { ...day, dinner: { ...day.dinner, status } } : day)),
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
    { dinner: { id: 'd4', name: 'Pot roast', status: 'regular', meal_source: 'cook' }, date: '2026-10-05', day: 'Monday', items: [{ id: 'i2', name: 'Chuck roast', qty: '3 lb', needed: true }] },
    { dinner: { id: 'd1', name: 'Beef tacos', status: 'regular', meal_source: 'cook' }, date: '2026-10-07', day: 'Wednesday', items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }] },
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
// archived says the server has archived Beef tacos; questArchived, Shrimp
// gumbo.
let archived: boolean
let questArchived: boolean
// While holding, the server takes each read of HELD as it comes, answering
// it as things are then, and keeps the answer back until release is called;
// held lists each release.
let holding: boolean
let held: Array<() => void>
// While listsHeld, the server takes each read of the Regulars or the Quest
// log and keeps it back until failLists, which answers each with a 503.
let listsHeld: boolean
let heldLists: Array<() => void>
const LISTS = ['GET /api/v1/dinners?status=regular', 'GET /api/v1/dinners?status=quest_log']
// checks is every "METHOD path?search" sent with cache 'no-store' — the
// recipe check (apiCheck), which the worker never answers.
let checks: string[]
// tacosRecipe is Beef tacos' detail as the server has it now.
let tacosRecipe: typeof TACOS_RECIPE
const HELD = [`/api/v1/plans/${WEEK}`, '/api/v1/home', `/api/v1/shopping/${WEEK}`, '/api/v1/dinners/d1']

// server answers "METHOD path?search" while it can be reached.
const server: Record<string, () => Response | Promise<Response>> = {
  'GET /api/v1/me': () => jsonResponse(200, ME),
  'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
  'GET /api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: archived ? [ROAST] : [TACOS, ROAST] }),
  'GET /api/v1/dinners?status=quest_log': () => jsonResponse(200, { dinners: questArchived ? [PAD_THAI] : [GUMBO, PAD_THAI] }),
  // Shrimp gumbo's detail is read only once another device archived it.
  'GET /api/v1/dinners/q1': () => (questArchived ? errorResponse(404, 'not_found') : Promise.reject(new Error('Shrimp gumbo read before it is archived'))),
  'GET /api/v1/suggestions/deck': () => jsonResponse(200, { suggestions: [], active_batch: null, failed_batch: null }),
  'GET /api/v1/dinners/d1': () => (archived ? errorResponse(404, 'not_found') : jsonResponse(200, tacosRecipe)),
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
  questArchived = false
  holding = false
  held = []
  listsHeld = false
  heldLists = []
  checks = []
  tacosRecipe = TACOS_RECIPE
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
  stubFetchThroughWorker(async (request) => {
    const url = new URL(request.url)
    const key = `${request.method} ${url.pathname}${url.search}`
    networkReads.push(key)
    if (request.cache === 'no-store') checks.push(key)
    if (!reachable) throw new TypeError('Failed to fetch')
    if (listsHeld && LISTS.includes(key)) {
      await new Promise<void>((fail) => heldLists.push(fail))
      return errorResponse(503, 'unavailable')
    }
    const respond = Object.hasOwn(server, key) ? server[key] : undefined
    if (respond === undefined) throw new Error(`unexpected network fetch: ${key}`)
    const response = await respond()
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
            <Route path="/discover" element={<DiscoverPage />} />
            <Route path="/dinner/:id" element={<DinnerDetailPage />} />
          </Routes>
          <nav>
            <Link to="/">To Home</Link>
            <Link to={PLAN_SCREEN}>To Plan</Link>
            <Link to={RUN_SCREEN}>To Supply run</Link>
            <Link to={REGULARS_SCREEN}>To Regulars</Link>
            <Link to={DISCOVER_SCREEN}>To Discover</Link>
            {/* Not a recipe link (recipeLinks): its search sets it apart. */}
            <Link to="/dinner/d1?typed=1">To Beef tacos</Link>
            <Link to="/dinner/q1?typed=1">To Shrimp gumbo</Link>
          </nav>
        </InDataRouter>
        <OfflinePill />
      </QueryClientProvider>
    </ApiProvider>,
  )
  return { queryClient, unmount }
}

// NAMES_BY_ID names the dinners recipeLinks looks for.
const NAMES_BY_ID: Record<string, string> = { d1: 'Beef tacos', d4: 'Pot roast' }

// recipeLinks lists every control on the page that opens dinner id's recipe
// (useOpenRecipe): a plan card's or Supply run card's button named for the
// dinner, Home's Recipe pill while tonight is the dinner, and any link
// straight to its detail, which no screen offers any more.
function recipeLinks(id: string) {
  const name = NAMES_BY_ID[id]!
  const tonight = screen.queryByRole('region', { name: 'Tonight' })
  const pill = tonight !== null && within(tonight).queryByRole('heading', { name }) !== null ? within(tonight).queryAllByRole('button', { name: 'Recipe' }) : []
  const links = screen.queryAllByRole('link').filter((link) => link.getAttribute('href') === `/dinner/${id}`)
  return [...screen.queryAllByRole('button', { name }), ...pill, ...links]
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
  await waitFor(() => expect(within(screen.getByRole('region', { name: 'Beef tacos' })).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument())
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
// longer have it and its detail is not found. Pot roast still links when the
// server answered the reads (from 'server'); answered from what the worker
// or this device saved (from 'saved'), nothing on the plan or Supply run
// links, as no status there is verified (F1, feature review round 3).
async function expectNoRecipeLink(from: 'server' | 'saved') {
  go('To Plan')
  await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })
  await waitFor(() => expect(screen.getByText('Pot roast')).toBeInTheDocument())
  if (from === 'server') await waitFor(() => expect(recipeLinks('d4')).toHaveLength(1))
  else expect(recipeLinks('d4')).toEqual([])
  expect(screen.getByText('Beef tacos')).toBeInTheDocument()
  expect(recipeLinks('d1')).toEqual([])

  go('To Home')
  const tonight = await screen.findByRole('region', { name: 'Tonight' })
  expect(within(tonight).getByRole('heading', { name: 'Beef tacos' })).toBeInTheDocument()
  expect(recipeLinks('d1')).toEqual([])

  go('To Supply run')
  const card = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
  expect(within(card).queryByRole('button')).not.toBeInTheDocument()
  expect(within(card).queryByRole('link')).not.toBeInTheDocument()
  expect(within(card).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
  const roast = screen.getByRole('region', { name: 'Pot roast' })
  if (from === 'server') expect(within(roast).getByRole('button', { name: 'Pot roast' })).toBeInTheDocument()
  else expect(within(roast).queryByRole('button')).not.toBeInTheDocument()

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
    await expectNoRecipeLink('saved')
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
    expect(recipeLinks('d4')).toEqual([])
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
    await expectNoRecipeLink('server')
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
    await expectNoRecipeLink('saved')
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
    await expectNoRecipeLink('saved')
    online.unmount()

    // A new page offline opens the saved Supply run: still no link.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(recipeLinks('d1')).toEqual([])
    runPage.unmount()

    // A new page that can reach the server sends the removal again; the
    // server answers it, and Beef tacos is marked archived, the saved Supply
    // run with it.
    server['DELETE /api/v1/dinners/d1'] = archiveTacos
    reachable = true
    networkReads = []
    await renderApp(RUN_SCREEN)
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED))
    expect(networkReads).toContain('DELETE /api/v1/dinners/d1')
    await waitFor(() => expect(savedStatus('d1')).toBe('archived'))
    expect(recipeLinks('d1')).toEqual([])
  })

  it('keeps the Regular hidden, linking to its recipe nowhere, while the server refuses it sent again, until it archives it', async () => {
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

    // The server can be reached again: sent the removal again, it refuses
    // it and still has Beef tacos, and every read says so. The mark stays,
    // so no screen links to its recipe.
    server['DELETE /api/v1/dinners/d1'] = () => errorResponse(500, 'internal')
    reachable = true
    networkReads = []
    await readAgain(online.queryClient)
    await waitFor(() => expect(networkReads).toContain('DELETE /api/v1/dinners/d1'))
    await waitFor(() => expect(online.queryClient.isFetching()).toBe(0))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    await expectNoRecipeLink('server')
    online.unmount()

    // Next time the app starts it sends the removal again; the server
    // archives Beef tacos, and it is marked archived for good.
    server['DELETE /api/v1/dinners/d1'] = archiveTacos
    networkReads = []
    await renderApp(PLAN_SCREEN)
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED))
    expect(networkReads).toContain('DELETE /api/v1/dinners/d1')
    await expectNoRecipeLink('server')
  })
})

// F3 (review round 7): this device's removal R1 is answered only after
// another device's removal R2 has archived Beef tacos. Its mark is never
// taken away, so whatever R1's answer, no screen links to the recipe.
describe('a removal answered only after another device archived the dinner', () => {
  it('links to its recipe nowhere — online, from what the worker and this device saved, or after a reload', async () => {
    mockViewport(768)
    const online = await renderApp(PLAN_SCREEN)
    await readOnline()
    expect(await savedRegular('d1')).toContain(`/api/v1/plans/${WEEK}`)

    // R1 reaches the server, which holds its answer back.
    let answerR1!: () => void
    server['DELETE /api/v1/dinners/d1'] = () =>
      new Promise<Response>((resolve) => {
        answerR1 = () => resolve(errorResponse(500, 'internal'))
      })
    removeTacos()
    await waitFor(() => expect(answerR1).toBeTypeOf('function'))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)

    // Another device's removal R2 commits: the server archives Beef tacos.
    archived = true
    // Only then is R1 answered, refused.
    answerR1()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not remove Beef tacos: internal message. It could not be removed here; it stays hidden on this device until the server settles it.',
    )
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    expect(screen.queryByRole('button', { name: 'Edit Beef tacos' })).not.toBeInTheDocument()
    // The Supply run saved on this device still has Beef tacos a Regular.
    expect(savedStatus('d1')).toBe('regular')

    // Offline, every read is the worker's from before R2, Beef tacos a
    // Regular in each, yet nothing links to its recipe.
    reachable = false
    networkReads = []
    await readAgain(online.queryClient)
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`, 'GET /api/v1/dinners/d1']),
    )
    expect(await savedRegular('d1')).toContain(`/api/v1/plans/${WEEK}`)
    await expectNoRecipeLink('saved')
    online.unmount()

    // A new page offline opens the Supply run saved on this device: no link,
    // its items still toggling.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    const reopened = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(reopened).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
    runPage.unmount()

    // A new page online sends R1 again; the server, which has Beef tacos
    // archived, archives it again and answers, so it is marked archived, the
    // saved Supply run with it, and still nothing links to the recipe.
    server['DELETE /api/v1/dinners/d1'] = archiveTacos
    reachable = true
    networkReads = []
    await renderApp(PLAN_SCREEN)
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED))
    expect(networkReads).toContain('DELETE /api/v1/dinners/d1')
    await waitFor(() => expect(savedStatus('d1')).toBe('archived'))
    await expectNoRecipeLink('server')
  })
})

describe('a removal another tab is still sending', () => {
  it('is not settled by another page while unanswered, and links nowhere after that tab closes and a reload', async () => {
    mockViewport(768)
    const tabA = await renderApp(PLAN_SCREEN)
    await readOnline()

    // Tab A's removal reaches the server, which has not archived Beef tacos
    // yet, and is never answered.
    server['DELETE /api/v1/dinners/d1'] = () => new Promise<Response>(() => undefined)
    removeTacos()
    await waitFor(() => expect(networkReads).toContain('DELETE /api/v1/dinners/d1'))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    tabA.unmount()

    // Tab B opens while tab A's removal is unanswered. The server still has
    // Beef tacos and says so to every read, yet tab B neither takes the mark
    // away nor sends the removal itself, and links nowhere to the recipe.
    networkReads = []
    const tabB = await renderApp(PLAN_SCREEN)
    await read(tabB.queryClient, `/api/v1/plans/${WEEK}`)
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(REMOVING)
    expect(networkReads).not.toContain('DELETE /api/v1/dinners/d1')
    expect(screen.getByText('Beef tacos')).toBeInTheDocument()
    expect(recipeLinks('d1')).toEqual([])

    // Tab A's removal commits, and tab A closes before its answer comes.
    // Tab B, holding the dinner's lock now, sends the removal again; the
    // server answers it, and Beef tacos is marked archived, the saved Supply
    // run with it.
    archived = true
    server['DELETE /api/v1/dinners/d1'] = archiveTacos
    deviceLocks().close(removalLock('d1'))
    await waitFor(() => expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED))
    expect(networkReads).toContain('DELETE /api/v1/dinners/d1')
    await waitFor(() => expect(savedStatus('d1')).toBe('archived'))
    tabB.unmount()

    // A new page offline opens the saved Supply run: its Beef tacos card
    // links nowhere, and its items still toggle.
    reachable = false
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    const card = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(card).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
    expect(localStorage.getItem(archivedKey('d1'))).toBe(ARCHIVED)
  })
})

// F1 (feature review round 3): another device archives Beef tacos. This
// device has no mark of it, and what the worker and this device saved still
// say it is a Regular: a status from such a read is not verified, so it
// offers no recipe link. The server's own answer offers it again, only to a
// dinner the server still has.
describe('a dinner another device archived, read here from what was saved before', () => {
  // expectLinks checks the plan, Home and Supply run link to the recipes of
  // exactly the dinners of ids among Beef tacos (d1) and Pot roast (d4).
  async function expectLinks(ids: string[]) {
    go('To Plan')
    await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })
    await waitFor(() => expect(screen.getByText('Pot roast')).toBeInTheDocument())
    expect(screen.getByText('Beef tacos')).toBeInTheDocument()
    expect(recipeLinks('d1')).toHaveLength(ids.includes('d1') ? 1 : 0)
    expect(recipeLinks('d4')).toHaveLength(ids.includes('d4') ? 1 : 0)

    go('To Home')
    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Beef tacos' })).toBeInTheDocument()
    expect(within(tonight).queryAllByRole('button', { name: 'Recipe' })).toHaveLength(ids.includes('d1') ? 1 : 0)
    expect(within(tonight).queryAllByRole('link')).toHaveLength(0)

    go('To Supply run')
    const tacos = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(tacos).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(within(tacos).queryAllByRole('button')).toHaveLength(ids.includes('d1') ? 1 : 0)
    expect(within(screen.getByRole('region', { name: 'Pot roast' })).queryAllByRole('button')).toHaveLength(ids.includes('d4') ? 1 : 0)
  }

  it('offers no recipe link from the worker’s or this device’s saved reads, and only the server’s answer offers one again, for a dinner it still has', async () => {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    await readOnline()
    await expectLinks(['d1', 'd4'])

    // Another device archives Beef tacos; this one never removed it.
    archived = true
    expect(localStorage.getItem(archivedKey('d1'))).toBeNull()

    // The server cannot be reached and everything is read again: the worker
    // answers from what it saved, Beef tacos a Regular in each, and no
    // dinner links to its recipe.
    reachable = false
    networkReads = []
    await readAgain(page.queryClient)
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`]),
    )
    expect(await savedRegular('d1')).toEqual(expect.arrayContaining(['/api/v1/home', `/api/v1/plans/${WEEK}`, `/api/v1/shopping/${WEEK}`]))
    await expectLinks([])
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    page.unmount()

    // A new page offline opens the Supply run saved on this device, Beef
    // tacos a Regular on it: no dinner links to its recipe.
    expect(savedStatus('d1')).toBe('regular')
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(recipeLinks('d1')).toEqual([])
    expect(recipeLinks('d4')).toEqual([])
    runPage.unmount()

    // The server can be reached again: its answers say Beef tacos is
    // archived, so only Pot roast links to its recipe.
    reachable = true
    const fresh = await renderApp(PLAN_SCREEN)
    await read(fresh.queryClient, `/api/v1/plans/${WEEK}`)
    await expectLinks(['d4'])
    await waitFor(() => expect(fresh.queryClient.isFetching()).toBe(0))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(localStorage.getItem(archivedKey('d1'))).toBeNull()
  })

  it('reads the plan, Home and Supply run again once a recipe link shown before the archive opens a dinner the server no longer has', async () => {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    await readOnline()
    go('To Plan')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))

    // Another device archives Beef tacos while its recipe button is on
    // screen here, from the server's answer of a minute ago. Opening it asks
    // the server, which says it is not found: no detail opens, the plan says
    // why in place and shows Beef tacos plain at once.
    archived = true
    vi.setSystemTime(new Date('2026-10-07T16:01:00Z'))
    networkReads = []
    fireEvent.click(recipeLinks('d1')[0]!)
    expect(await screen.findByRole('alert')).toHaveTextContent('Beef tacos was removed, so its recipe is gone.')
    expect(checks).toEqual(['GET /api/v1/dinners/d1'])
    expect(recipeLinks('d1')).toEqual([])
    expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()

    // Back on the plan, Home and Supply run, each is read again, and only
    // Pot roast links.
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))
    await expectLinks(['d4'])
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`]),
    )
    await expectLinks(['d4'])
  })

  it('offers each recipe link again once the server answers that the dinner is still there', async () => {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    await readOnline()

    // Offline, every read is what the worker saved: no recipe links.
    reachable = false
    await readAgain(page.queryClient)
    await expectLinks([])

    // The server answers again and still has every dinner: each links.
    reachable = true
    await readAgain(page.queryClient)
    await expectLinks(['d1', 'd4'])
  })
})

// The Regulars and the Quest log follow the same rule (Maestro 440ddb44):
// a row opens its dinner's recipe only from the server's answer. Another
// device archives Beef tacos (a Regular) and Shrimp gumbo (in the Quest
// log); this device has no mark of either, and what the worker saved still
// lists both.
describe('a Regular or Quest-log dinner another device archived, read here from what was saved before', () => {
  // opening lists the names of the rows in list that open their dinner's
  // recipe: the row's own button, not its pencil, heart or remove button.
  function opening(list: HTMLElement) {
    return within(list)
      .getAllByRole('listitem')
      .map((row) => {
        const name = within(row).getByRole('button', { name: /^(Edit|Fix recipe for|Remove) / }).getAttribute('aria-label')!
        return name.replace(/^(Edit|Fix recipe for|Remove) /, '')
      })
      .filter((name) => within(list).queryByRole('button', { name: new RegExp(`^${name}`) }) !== null)
  }

  async function regulars() {
    await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
    return screen.getByRole('list', { name: 'Regulars' })
  }

  async function questLog(count: number) {
    fireEvent.click(await screen.findByRole('button', { name: `Quest log · ${count}` }))
    return within(screen.getByRole('dialog', { name: 'Quest log' })).getByRole('list', { name: 'Quest log' })
  }

  it('offers no recipe from the Regulars or the Quest log the worker saved, keeps their other controls, and the server’s answer offers it again for a dinner it still has', async () => {
    mockViewport(768)
    // Online, each list links every row, and the worker saves each read.
    let page = await renderApp(REGULARS_SCREEN)
    expect(opening(await regulars())).toEqual(['Beef tacos', 'Pot roast'])
    page.unmount()
    page = await renderApp(DISCOVER_SCREEN)
    expect(opening(await questLog(2))).toEqual(['Shrimp gumbo', 'Pad thai'])
    page.unmount()

    archived = true
    questArchived = true
    expect(localStorage.getItem(archivedKey('d1'))).toBeNull()
    expect(localStorage.getItem(archivedKey('q1'))).toBeNull()

    // Offline, a new page reads each list from what the worker saved: every
    // row shows, none opens a recipe, and the pencil, heart and remove
    // buttons work as before.
    reachable = false
    networkReads = []
    page = await renderApp(REGULARS_SCREEN)
    const saved = await regulars()
    expect(networkReads).toContain('GET /api/v1/dinners?status=regular')
    expect(within(saved).getAllByRole('listitem')).toHaveLength(2)
    expect(opening(saved)).toEqual([])
    expect(screen.getByRole('button', { name: 'Favorite Beef tacos' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    expect(await screen.findByRole('button', { name: 'Remove from Regulars' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    page.unmount()

    page = await renderApp(DISCOVER_SCREEN)
    const savedLog = await questLog(2)
    expect(networkReads).toContain('GET /api/v1/dinners?status=quest_log')
    expect(opening(savedLog)).toEqual([])
    fireEvent.click(within(savedLog).getByRole('button', { name: 'Remove Shrimp gumbo' }))
    expect(screen.getByRole('dialog', { name: 'Remove Shrimp gumbo?' })).toBeInTheDocument()
    page.unmount()

    // The server answers again: only the dinners it still has show, and each
    // opens its recipe.
    reachable = true
    page = await renderApp(REGULARS_SCREEN)
    await waitFor(() => expect(opening(screen.getByRole('list', { name: 'Regulars' }))).toEqual(['Pot roast']))
    page.unmount()
    page = await renderApp(DISCOVER_SCREEN)
    expect(opening(await questLog(1))).toEqual(['Pad thai'])
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('stops offering each list’s recipes once a read again comes from what the worker saved, and offers them again once the server answers', async () => {
    mockViewport(768)
    const page = await renderApp(REGULARS_SCREEN)
    expect(opening(await regulars())).toEqual(['Beef tacos', 'Pot roast'])
    go('To Discover')
    const log = await questLog(2)
    expect(opening(log)).toEqual(['Shrimp gumbo', 'Pad thai'])

    // Offline, both lists are read again from what the worker saved.
    reachable = false
    await Promise.all(
      [REGULARS_KEY, QUEST_LOG_KEY].map((queryKey) => page.queryClient.refetchQueries({ queryKey, type: 'all' })),
    )
    await waitFor(() => expect(opening(log)).toEqual([]))
    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Quest log' }), { key: 'Escape' })
    go('To Regulars')
    expect(opening(await regulars())).toEqual([])

    // The server answers again and still has every dinner: each row opens.
    reachable = true
    await Promise.all(
      [REGULARS_KEY, QUEST_LOG_KEY].map((queryKey) => page.queryClient.refetchQueries({ queryKey, type: 'all' })),
    )
    await waitFor(() => expect(opening(screen.getByRole('list', { name: 'Regulars' }))).toEqual(['Beef tacos', 'Pot roast']))
    go('To Discover')
    expect(opening(await questLog(2))).toEqual(['Shrimp gumbo', 'Pad thai'])
  })

  it('reads the Regulars and the Quest log again once a row read before the archive opens a dinner the server no longer has', async () => {
    mockViewport(768)
    const page = await renderApp(REGULARS_SCREEN)
    await regulars()
    go('To Discover')
    await questLog(2)
    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Quest log' }), { key: 'Escape' })

    // A minute later — the details no longer fresh — both lists are read
    // again from the server, then another device archives both dinners.
    vi.setSystemTime(new Date('2026-10-07T16:01:00Z'))
    go('To Regulars')
    await read(page.queryClient, '/api/v1/dinners?status=regular')
    expect(opening(await regulars())).toEqual(['Beef tacos', 'Pot roast'])
    go('To Discover')
    await read(page.queryClient, '/api/v1/dinners?status=quest_log')
    const log = await questLog(2)
    archived = true
    questArchived = true

    // Shrimp gumbo's row asks the server for a dinner it no longer has: no
    // detail opens, the sheet says why, and the row is gone at once; the
    // Quest log, on screen, is read again.
    networkReads = []
    fireEvent.click(within(log).getByRole('button', { name: /^Shrimp gumbo/ }))
    const sheet = screen.getByRole('dialog', { name: 'Quest log' })
    expect(await within(sheet).findByRole('alert')).toHaveTextContent('Shrimp gumbo was removed, so its recipe is gone.')
    expect(checks).toEqual(['GET /api/v1/dinners/q1'])
    expect(opening(log)).toEqual(['Pad thai'])
    expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()
    await waitFor(() => expect(networkReads).toContain('GET /api/v1/dinners?status=quest_log'))
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))
    fireEvent.keyDown(sheet, { key: 'Escape' })

    // Back on the Regulars, read seconds ago, its list is read again, and
    // Beef tacos' row — read before the archive — is gone with it.
    networkReads = []
    go('To Regulars')
    await waitFor(() => expect(networkReads).toContain('GET /api/v1/dinners?status=regular'))
    await waitFor(() => expect(opening(screen.getByRole('list', { name: 'Regulars' }))).toEqual(['Pot roast']))
    go('To Discover')
    expect(opening(await questLog(1))).toEqual(['Pad thai'])
  })
})

// F1-1, F1-2 and F1-3 (Maestro 440ddb44, review round 1 of PR #6): a page
// read from the server may stay open while another device archives one of
// its dinners. Its recipe opens only once the server, asked when it is
// activated, answers that the dinner is there (useOpenRecipe) — never from
// the worker's cache, nor from a detail this page read moments ago — and a
// 404 opens nothing and leaves no read here offering that recipe.
describe('a recipe opened from a page read before another device archived its dinner', () => {
  // rowButton is a Regulars or Quest-log row's own button, the one that
  // opens its recipe, or null when the row is plain or gone.
  function rowButton(list: HTMLElement, name: string) {
    return within(list).queryByRole('button', { name: new RegExp(`^${name}`) })
  }

  async function regulars() {
    await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
    return screen.getByRole('list', { name: 'Regulars' })
  }

  async function questLog(count: number) {
    fireEvent.click(await screen.findByRole('button', { name: `Quest log · ${count}` }))
    return within(screen.getByRole('dialog', { name: 'Quest log' })).getByRole('list', { name: 'Quest log' })
  }

  // Each screen, read from the server, offers Beef tacos' recipe (Shrimp
  // gumbo's on the Quest log); open finds that control.
  it.each([
    {
      screenName: 'plan',
      path: PLAN_SCREEN,
      id: 'd1',
      open: () =>
        waitFor(() => {
          const [control] = recipeLinks('d1')
          expect(control).toBeDefined()
          return control!
        }),
    },
    {
      screenName: 'Home',
      path: '/',
      id: 'd1',
      open: async () => within(await screen.findByRole('region', { name: 'Tonight' })).findByRole('button', { name: 'Recipe' }),
    },
    {
      screenName: 'Supply run',
      path: RUN_SCREEN,
      id: 'd1',
      open: async () => within(await screen.findByRole('region', { name: 'Beef tacos' })).findByRole('button', { name: 'Beef tacos' }),
    },
    { screenName: 'Regulars', path: REGULARS_SCREEN, id: 'd1', open: async () => rowButton(await regulars(), 'Beef tacos')! },
    { screenName: 'Quest log', path: DISCOVER_SCREEN, id: 'q1', open: async () => rowButton(await questLog(2), 'Shrimp gumbo')! },
  ])(
    'asks the server when the $screenName’s recipe is activated, and on its 404 opens nothing and offers it no more',
    async ({ path, id, open }) => {
      mockViewport(768)
      const page = await renderApp(path)
      const control = await open()
      await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))

      // Another device archives the dinner while this page stays open; no
      // read here is made again.
      archived = true
      questArchived = true
      networkReads = []
      fireEvent.click(control)

      // The server is asked, with nothing cached, and answers 404: the page
      // stays, says why in place, and no longer offers that recipe — before
      // any read again is answered.
      const name = id === 'd1' ? 'Beef tacos' : 'Shrimp gumbo'
      expect(await screen.findByRole('alert')).toHaveTextContent(`${name} was removed, so its recipe is gone.`)
      expect(checks).toEqual([`GET /api/v1/dinners/${id}`])
      expect(control).not.toBeInTheDocument()
      if (id === 'd1') expect(recipeLinks('d1')).toEqual([])
      else expect(rowButton(screen.getByRole('list', { name: 'Quest log' }), 'Shrimp gumbo')).toBeNull()
      expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()
      expect(screen.queryByText('Brown the beef.')).not.toBeInTheDocument()
    },
  )

  it('asks the server even for a detail this page read seconds ago, and shows what the server answers', async () => {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    // Beef tacos' detail is read and still fresh here.
    await readOnline()
    go('To Plan')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))

    // The recipe changes on another device: opening it shows the server's
    // answer, not the detail read seconds ago.
    tacosRecipe = { ...TACOS_RECIPE, steps: ['Brown the beef in a hot pan.'] }
    fireEvent.click(recipeLinks('d1')[0]!)
    expect(await screen.findByText('Brown the beef in a hot pan.')).toBeInTheDocument()
    expect(screen.queryByText('Brown the beef.')).not.toBeInTheDocument()
    expect(checks).toEqual(['GET /api/v1/dinners/d1'])

    // Back on the plan, its detail just checked and fresh, another device
    // archives it: opening it asks the server again, which says it is gone.
    go('To Plan')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    archived = true
    fireEvent.click(recipeLinks('d1')[0]!)
    expect(await screen.findByRole('alert')).toHaveTextContent('Beef tacos was removed, so its recipe is gone.')
    expect(checks).toEqual(['GET /api/v1/dinners/d1', 'GET /api/v1/dinners/d1'])
    expect(screen.queryByText('Brown the beef in a hot pan.')).not.toBeInTheDocument()
    expect(recipeLinks('d1')).toEqual([])
  })

  it('opens nothing when the server cannot be asked, not even the detail the worker saved, and opens it once it can', async () => {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    await readOnline()
    expect(await savedRegular('d1')).toContain('/api/v1/dinners/d1')
    go('To Plan')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))

    // The connection drops before anything is read again.
    reachable = false
    fireEvent.click(recipeLinks('d1')[0]!)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't check the recipe for Beef tacos with the server, so it didn't open.")
    expect(checks).toEqual(['GET /api/v1/dinners/d1'])
    expect(screen.queryByText('Brown the beef.')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()

    // Back online, trying again opens it from the server's answer.
    reachable = true
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Brown the beef.')).toBeInTheDocument()
    expect(checks).toEqual(['GET /api/v1/dinners/d1', 'GET /api/v1/dinners/d1'])
  })

  it('takes a dinner the server says is gone out of the Regulars and the Quest log at once, through a read again held and then failed', async () => {
    mockViewport(768)
    const page = await renderApp(REGULARS_SCREEN)
    expect(rowButton(await regulars(), 'Beef tacos')).not.toBeNull()
    go('To Discover')
    expect(rowButton(await questLog(2), 'Shrimp gumbo')).not.toBeNull()
    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Quest log' }), { key: 'Escape' })

    // Another device archives both; the Regulars and Quest log reads here
    // are kept back from now on.
    archived = true
    questArchived = true
    listsHeld = true

    // Beef tacos' detail is read: the server says it is not found. Back on
    // the Regulars, read again but not yet answered, its row is gone.
    go('To Beef tacos')
    expect(await screen.findByText('Dinner not found')).toBeInTheDocument()
    networkReads = []
    go('To Regulars')
    const list = await regulars()
    await waitFor(() => expect(networkReads).toContain('GET /api/v1/dinners?status=regular'))
    expect(rowButton(list, 'Beef tacos')).toBeNull()
    expect(rowButton(list, 'Pot roast')).not.toBeNull()

    // So with Shrimp gumbo on the Quest log.
    go('To Shrimp gumbo')
    expect(await screen.findByText('Dinner not found')).toBeInTheDocument()
    go('To Discover')
    const log = await questLog(1)
    await waitFor(() => expect(networkReads).toContain('GET /api/v1/dinners?status=quest_log'))
    expect(rowButton(log, 'Shrimp gumbo')).toBeNull()
    expect(rowButton(log, 'Pad thai')).not.toBeNull()
    expect(heldLists.length).toBeGreaterThan(0)

    // The reads again fail: neither list holds the dinner the server said
    // is gone.
    heldLists.splice(0).forEach((fail) => fail())
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))
    const held = page.queryClient
      .getQueriesData<Array<{ id: string }>>({ queryKey: REGULARS_KEY })
      .flatMap(([, data]) => (Array.isArray(data) ? data.map((dinner) => dinner.id) : []))
    expect(held).toEqual(['d4'])
    expect(page.queryClient.getQueryData<Array<{ id: string }>>(QUEST_LOG_KEY)!.map((dinner) => dinner.id)).toEqual(['q2'])
  })
})

// F1 (Maestro 95e95230, feature review round 5): the server reads Beef tacos'
// detail, which it still has, and its answer is kept back while another tab
// of the app removes Beef tacos. The answer arriving after the mark opens
// nothing: the device's mark, read again once the check is answered, says
// the dinner is gone, whether or not the other tab's storage event has
// arrived yet.
describe('a recipe check answered after another tab removed its dinner', () => {
  // holdCheck keeps the server's answer to Beef tacos' recipe check back
  // until the returned release is called; the answer is the one the server
  // read when the check reached it.
  function holdCheck(): () => void {
    let release: (() => void) | undefined
    server['GET /api/v1/dinners/d1'] = () => {
      const answer = jsonResponse(200, tacosRecipe)
      return new Promise<Response>((resolve) => {
        release = () => resolve(answer)
      })
    }
    return () => release!()
  }
  const answerTacos = server['GET /api/v1/dinners/d1']!
  afterEach(() => {
    server['GET /api/v1/dinners/d1'] = answerTacos
  })

  async function planWithRecipe() {
    mockViewport(768)
    const page = await renderApp(PLAN_SCREEN)
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    await waitFor(() => expect(page.queryClient.isFetching()).toBe(0))
    return page
  }

  it.each([
    ['before', true],
    ['after', false],
  ])(
    'opens nothing, caches nothing and says it was removed, with the other tab’s storage event arriving %s the answer',
    async (_, eventFirst) => {
      const page = await planWithRecipe()
      const release = holdCheck()
      fireEvent.click(recipeLinks('d1')[0]!)
      await waitFor(() => expect(checks).toEqual(['GET /api/v1/dinners/d1']))

      // Another tab removes Beef tacos while the check is out.
      localStorage.setItem(archivedKey('d1'), REMOVING)
      const event = new StorageEvent('storage', { key: archivedKey('d1'), newValue: REMOVING })
      if (eventFirst) window.dispatchEvent(event)
      release()

      expect(await screen.findByRole('alert')).toHaveTextContent('Beef tacos was removed, so its recipe is gone.')
      if (!eventFirst) window.dispatchEvent(event)
      expect(screen.getByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
      expect(screen.queryByText('Brown the beef.')).not.toBeInTheDocument()
      expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()
      expect(page.queryClient.getQueryData(dinnerDetailKey('d1'))).toBeUndefined()
      await waitFor(() => expect(recipeLinks('d1')).toEqual([]))
    },
  )

  it('still opens the recipe once its held answer arrives when no tab removed it', async () => {
    const page = await planWithRecipe()
    const release = holdCheck()
    fireEvent.click(recipeLinks('d1')[0]!)
    await waitFor(() => expect(checks).toEqual(['GET /api/v1/dinners/d1']))
    release()

    expect(await screen.findByText('Brown the beef.')).toBeInTheDocument()
    expect(page.queryClient.getQueryData(dinnerDetailKey('d1'))).toMatchObject({ id: 'd1' })
  })
})
