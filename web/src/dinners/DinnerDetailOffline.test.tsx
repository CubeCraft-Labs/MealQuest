// A dinner's detail offline once visited: the page reads through the service
// worker's real API strategy (src/test/serviceWorker.ts), so what it shows
// offline is what the worker saved while it was online. jsdom has no service
// worker, so the strategy runs directly against an in-memory cache.
import { render, screen, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OfflinePill } from '../pwa/OfflinePill.tsx'
import { AppShell } from '../shell/AppShell.tsx'
import { cacheNames, routeFor } from '../sw/routes.ts'
import { ApiProviders, HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { errorResponse, jsonResponse } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { origin, stubFetchThroughWorker, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'

// grounded is a dinner as GET /api/v1/dinners/{id} sends it, its recipe found.
function grounded(id: string, name: string) {
  return {
    id,
    name,
    status: 'quest_log',
    origin: 'suggested',
    method: 'stovetop',
    minutes_total: 30,
    difficulty: 'easy',
    cuisine: 'korean',
    is_favorite: false,
    photo_urls: { '400': `/media/photos/${id}-400.jpg`, '1200': `/media/photos/${id}-1200.jpg` },
    ingredients: [
      { name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' },
      { name: 'Jasmine rice', qty: '2', unit: 'cups', note: null, raw_text: '2 cups jasmine rice' },
    ],
    steps: ['Cook the rice.', 'Brown the beef with the sauce.'],
    source: { site_name: 'Serious Eats' },
    grounding_status: 'grounded',
    grounding_error: null,
    why_picked: null,
  }
}

// server is what the server sends while it can be reached. Kitchen reading
// mode also reads the household settings, which the worker never saves, and
// the week's plans to find the dinner's day, of which there are none here.
const server: Record<string, unknown> = {
  '/api/v1/settings/household': HOUSEHOLD_SETTINGS,
  '/api/v1/dinners/d1': grounded('d1', 'Korean beef bowls'),
  '/api/v1/dinners/d2': grounded('d2', 'Chicken tikka masala'),
  '/api/v1/dinners/d3': {
    ...grounded('d3', 'Pork carnitas'),
    photo_urls: null,
    ingredients: [],
    steps: [],
    source: null,
    grounding_status: 'pending',
  },
}

let cache: FakeCache
let reachable: boolean
// networkReads is every path the network was asked for, reached or not.
let networkReads: string[]

beforeEach(() => {
  cache = stubWorkerCaches()
  reachable = true
  networkReads = []
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
  stubFetchThroughWorker(async (request) => {
    const path = new URL(request.url).pathname
    networkReads.push(path)
    if (!reachable) throw new TypeError('Failed to fetch')
    if (path.startsWith('/api/v1/plans/')) return errorResponse(404, 'not_found')
    if (!Object.hasOwn(server, path)) throw new Error(`unexpected network fetch: ${request.url}`)
    return jsonResponse(200, server[path])
  })
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

// open loads /dinner/{id} as a fresh page load would: a new API client,
// network status and query cache, with the offline indicator App shows.
function open(id: string) {
  const router = createMemoryRouter(
    [{ element: <AppShell />, children: [{ path: '/dinner/:id', element: <DinnerDetailPage /> }] }],
    { initialEntries: [`/dinner/${id}`] },
  )
  return render(
    <ApiProviders>
      <RouterProvider router={router} />
      <OfflinePill />
    </ApiProviders>,
  )
}

// visit opens a dinner while online and leaves once its title shows.
async function visit(id: string, name: string) {
  const page = open(id)
  expect(await screen.findByRole('heading', { level: 1, name })).toBeInTheDocument()
  expect(screen.queryByText('Offline — showing saved data')).not.toBeInTheDocument()
  page.unmount()
}

describe('dinner detail offline', () => {
  it('saves a visited dinner in the API cache the worker reads offline', async () => {
    mockViewport(390)
    expect(routeFor({ method: 'GET', url: new URL('/api/v1/dinners/d1', origin), mode: 'cors' }, origin)).toEqual({
      strategy: 'network-first',
      cacheName: cacheNames.api,
    })
    await visit('d1', 'Korean beef bowls')
    expect([...cache.entries.keys()]).toEqual([`${origin}/api/v1/dinners/d1`])
  })

  it('shows a visited dinner offline from its saved copy: photo, ingredients and steps, with the offline indicator', async () => {
    mockViewport(390)
    await visit('d1', 'Korean beef bowls')

    reachable = false
    open('d1')
    expect(await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    expect(screen.getByRole('img', { name: 'Korean beef bowls' })).toHaveAttribute('src', '/media/photos/d1-400.jpg')
    const [ingredients, steps] = screen.getAllByRole('list')
    expect(within(ingredients!).getAllByRole('listitem').map((row) => row.textContent)).toEqual([
      'Ground beef1 lb',
      'Jasmine rice2 cups',
    ])
    expect(within(steps!).getAllByRole('listitem').map((row) => row.textContent)).toEqual([
      '1Cook the rice.',
      '2Brown the beef with the sauce.',
    ])
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Dinner loading')).not.toBeInTheDocument()
  })

  it('shows a visited dinner offline in Kitchen reading mode on a tablet', async () => {
    mockViewport(1180)
    await visit('d1', 'Korean beef bowls')

    reachable = false
    open('d1')
    expect(await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })).toBeInTheDocument()
    expect(screen.getByText('Offline — showing saved data')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Korean beef bowls' })).toHaveAttribute('src', '/media/photos/d1-400.jpg')
    expect(within(screen.getByRole('region', { name: 'Ingredients' })).getAllByRole('checkbox')).toHaveLength(2)
    expect(screen.getByRole('listitem', { current: 'step' })).toHaveTextContent('Cook the rice.')
  })

  it('shows an error, not a blank page or placeholder, for a dinner never visited', async () => {
    mockViewport(390)
    await visit('d1', 'Korean beef bowls')

    reachable = false
    open('d2')
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load this dinner.")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
    expect(screen.queryByText(/Korean beef bowls|Chicken tikka masala/)).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Ingredients' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Dinner loading')).not.toBeInTheDocument()
    expect(screen.getByText('Offline — showing saved data')).toBeInTheDocument()
  })

  it('keeps a saved copy still finding its recipe as that, not a finished recipe', async () => {
    mockViewport(390)
    await visit('d3', 'Pork carnitas')

    reachable = false
    open('d3')
    expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Ingredients' })).not.toBeInTheDocument()
    expect(screen.getByText('Offline — showing saved data')).toBeInTheDocument()
    // Read once online and tried once offline before the saved copy answered.
    expect(networkReads.filter((path) => path.startsWith('/api/v1/dinners/'))).toEqual([
      '/api/v1/dinners/d3',
      '/api/v1/dinners/d3',
    ])
  })
})
