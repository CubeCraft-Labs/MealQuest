import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient, createVerifiedSession } from './api/index.ts'
import { createNetworkStatus } from './pwa/networkStatus.ts'
import { mockViewport } from './test/matchMedia.ts'
import { errorResponse, fakeLocation, jsonResponse, ME, request, stubFetch, stubFetchByPath } from './test/fetchStub.ts'
import App, { routes } from './App.tsx'
import { AI_SETTINGS, HOUSEHOLD_SETTINGS, RECIPE_SEARCH_SETTINGS, SETTINGS_API } from './test/ApiProviders.tsx'
import { InDataRouter } from './test/dataRouter.tsx'
import { dinnerBody } from './test/dinners.ts'

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function renderAt(path: string, location = fakeLocation(path)) {
  return { location, ...renderAppAt(path, location) }
}

function renderAppAt(path: string, location = fakeLocation(path)) {
  return render(
    <InDataRouter path={path}>
      <App api={createApiClient(location, createNetworkStatus(window), createVerifiedSession(localStorage))} serviceWorker={undefined} />
    </InDataRouter>,
  )
}

// The routes that render a built screen rather than a placeholder page.
const SCREENS = {
  '/onboarding': true,
  '/settings': true,
  '/settings/ai': true,
  '/settings/household': true,
  '/settings/regulars': true,
}

// SIGNED_IN answers what every route reads first: the session, then the
// household settings, onboarding finished.
const SIGNED_IN = {
  '/api/v1/me': () => jsonResponse(200, ME),
  '/api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
}

describe('App routes', () => {
  it('registers every handoff route plus the extra settings pages', () => {
    expect(routes.map((route) => route.path)).toEqual([
      '/',
      '/onboarding',
      '/plan',
      '/plan/shopping',
      '/plan/shopping/archive',
      '/discover',
      '/quick',
      '/cook-from-pantry',
      '/dinner/:id',
      '/settings',
      '/settings/notifications',
      '/settings/ai',
      '/settings/household',
      '/settings/regulars',
      '/settings/calendar',
    ])
  })

  it.each(routes.filter(({ path }) => !Object.hasOwn(SCREENS, path)).map(({ path, title }) => [path.replace(':id', 'abc123'), title]))(
    '%s renders its placeholder page inside the shell',
    async (path, title) => {
      mockViewport(390)
      stubFetchByPath(SIGNED_IN)
      renderAt(path)
      const heading = await screen.findByRole('heading', { level: 1, name: title })
      expect(screen.getByRole('main')).toContainElement(heading)
      expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    },
  )

  it('/settings renders the settings home inside the shell', async () => {
    mockViewport(390)
    stubFetchByPath({
      '/api/v1/me': () => jsonResponse(200, ME),
      ...SETTINGS_API,
    })
    renderAt('/settings')
    const heading = await screen.findByRole('heading', { level: 1, name: 'Settings' })
    expect(screen.getByRole('main')).toContainElement(heading)
    expect(await screen.findByText('Anthropic · Claude Sonnet')).toBeInTheDocument()
  })

  it('/settings/ai renders the AI assistant screen inside the shell', async () => {
    mockViewport(390)
    stubFetchByPath({
      ...SIGNED_IN,
      '/api/v1/settings/ai': () => jsonResponse(200, AI_SETTINGS),
      '/api/v1/settings/ai/models': () => jsonResponse(200, { models: [] }),
      '/api/v1/settings/recipe-search': () => jsonResponse(200, RECIPE_SEARCH_SETTINGS),
    })
    renderAt('/settings/ai')
    const heading = await screen.findByRole('heading', { level: 1, name: 'AI assistant' })
    expect(screen.getByRole('main')).toContainElement(heading)
    expect(await screen.findByText('••••••••3f9a')).toBeInTheDocument()
  })

  it('/settings/household renders the household settings screen inside the shell', async () => {
    mockViewport(390)
    stubFetchByPath(SIGNED_IN)
    renderAt('/settings/household')
    const heading = await screen.findByRole('heading', { level: 1, name: 'Household' })
    expect(screen.getByRole('main')).toContainElement(heading)
    expect(await screen.findByLabelText('Dinner time')).toHaveValue('17:30')
  })

  it('/settings/regulars renders the Regular dinners screen inside the shell', async () => {
    mockViewport(390)
    stubFetchByPath({
      ...SIGNED_IN,
      '/api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: [dinnerBody('d1', 'Beef tacos')] }),
    })
    renderAt('/settings/regulars')
    const heading = await screen.findByRole('heading', { level: 1, name: 'Regular dinners' })
    expect(screen.getByRole('main')).toContainElement(heading)
    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
  })
})

describe('App first-run redirect', () => {
  const FIRST_RUN = {
    '/api/v1/me': () => jsonResponse(200, ME),
    '/api/v1/settings/household': () => jsonResponse(200, { ...HOUSEHOLD_SETTINGS, onboarding_completed_at: null }),
  }

  it.each(routes.filter(({ path }) => path !== '/onboarding').map(({ path }) => path.replace(':id', 'abc123')))(
    '%s redirects to /onboarding while onboarding is not finished',
    async (path) => {
      mockViewport(390)
      stubFetchByPath(FIRST_RUN)
      renderAt(path)
      expect(await screen.findByRole('heading', { level: 1, name: 'Welcome to MealQuest' })).toBeInTheDocument()
      expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
    },
  )

  it('stays on an onboarding step', async () => {
    mockViewport(390)
    stubFetchByPath({ ...FIRST_RUN, '/api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: [] }) })
    renderAt('/onboarding/regulars')
    expect(await screen.findByRole('heading', { level: 1, name: "What's on your usual rotation?" })).toBeInTheDocument()
  })

  it('opens the routes once onboarding is finished', async () => {
    mockViewport(390)
    stubFetchByPath(SIGNED_IN)
    renderAt('/plan')
    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
  })

  it('reports a household settings failure with a retry instead of rendering the routes', async () => {
    mockViewport(390)
    stubFetch(jsonResponse(200, ME), errorResponse(500, 'internal'), jsonResponse(200, HOUSEHOLD_SETTINGS))
    renderAt('/plan')

    expect(await screen.findByRole('alert')).toHaveTextContent('MealQuest could not load: internal message')
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
    screen.getByRole('button', { name: 'Retry' }).click()
    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
  })
})

describe('App bootstrap', () => {
  it('shows the skeleton until /me loads, then the routes', async () => {
    mockViewport(390)
    const fetchMock = stubFetch(jsonResponse(200, ME), jsonResponse(200, HOUSEHOLD_SETTINGS))
    renderAt('/plan')

    expect(screen.getByRole('main', { name: 'Loading MealQuest' })).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()

    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
    expect(screen.queryByRole('main', { name: 'Loading MealQuest' })).not.toBeInTheDocument()
    expect(request(fetchMock, 0)).toMatchObject({ path: '/api/v1/me', method: 'GET', credentials: 'same-origin' })
  })

  it('sends a signed-out visitor to sign-in and never renders the routes', async () => {
    const fetchMock = stubFetch(errorResponse(401, 'unauthenticated'))
    const { location } = renderAt('/discover', fakeLocation('/discover', '?swipe=1'))

    await vi.waitFor(() => expect(location.assign).toHaveBeenCalledWith('/auth/login?return=%2Fdiscover%3Fswipe%3D1'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('main', { name: 'Loading MealQuest' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
  })

  it('shows the not-in-this-household screen on a 403', async () => {
    const fetchMock = stubFetch(errorResponse(403, 'forbidden'))
    const { location } = renderAt('/')

    expect(await screen.findByText(/not in this household/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Not in this household' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(location.assign).not.toHaveBeenCalled()
  })

  it('reports a /me failure with a retry instead of rendering the routes', async () => {
    mockViewport(390)
    stubFetch(errorResponse(500, 'internal'), jsonResponse(200, ME), jsonResponse(200, HOUSEHOLD_SETTINGS))
    renderAt('/')

    expect(await screen.findByRole('alert')).toHaveTextContent('MealQuest could not load: internal message')
    screen.getByRole('button', { name: 'Retry' }).click()
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
  })
})

describe('App offline bootstrap', () => {
  let onLine: boolean

  function goOffline() {
    onLine = false
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
  }

  // visitOnline is an earlier online visit on this device: /me answers with a
  // member, then the app is closed.
  async function visitOnline() {
    stubFetchByPath(SIGNED_IN)
    const { unmount } = renderAt('/')
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
    unmount()
  }

  beforeEach(() => {
    mockViewport(390)
    onLine = true
    vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => onLine)
  })

  it.each([
    ['/plan', 'Plan'],
    ['/dinner/01J9ZK', 'Dinner'],
  ])('an offline reload of %s after an online visit opens the saved page', async (path, title) => {
    await visitOnline()
    goOffline()
    renderAt(path)

    expect(await screen.findByRole('heading', { level: 1, name: title })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('opens the saved pages when the server is unreachable but the browser reports online', async () => {
    await visitOnline()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    renderAt('/plan')

    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
  })

  it('reports the failure offline when this device never verified a session', async () => {
    goOffline()
    renderAt('/plan')

    expect(await screen.findByRole('alert')).toHaveTextContent('MealQuest could not load: Failed to fetch')
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
  })

  it('reports the failure offline after the session was refused', async () => {
    await visitOnline()
    stubFetch(errorResponse(401, 'unauthenticated'))
    const { location, unmount } = renderAt('/plan')
    await vi.waitFor(() => expect(location.assign).toHaveBeenCalled())
    unmount()

    goOffline()
    renderAt('/plan')
    expect(await screen.findByRole('alert')).toHaveTextContent('MealQuest could not load: Failed to fetch')
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
  })

  it('loads /me again on reconnect and refuses writes until it has', async () => {
    await visitOnline()
    goOffline()
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    render(
      <InDataRouter path="/plan">
        <App api={api} serviceWorker={undefined} />
      </InDataRouter>,
    )
    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
    await expect(api.apiFetch('/api/v1/plans', { method: 'POST', body: {} })).rejects.toThrow(/no CSRF token/)

    const fetchMock = stubFetchByPath(SIGNED_IN)
    onLine = true
    act(() => {
      window.dispatchEvent(new Event('online'))
    })
    await vi.waitFor(() =>
      expect(fetchMock.mock.calls.map(([input]) => String(input)).sort()).toEqual([
        '/api/v1/me',
        '/api/v1/settings/household',
      ]),
    )
    await vi.waitFor(() => expect(screen.queryByRole('status')).toBeNull())
    expect(screen.getByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
  })
})
