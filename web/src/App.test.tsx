import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from './api/index.ts'
import { mockViewport } from './test/matchMedia.ts'
import { errorResponse, fakeLocation, jsonResponse, ME, request, stubFetch } from './test/fetchStub.ts'
import App, { routes } from './App.tsx'

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

function renderAt(path: string, location = fakeLocation(path)) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={createApiClient(location)} />
    </MemoryRouter>,
  )
  return location
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

  it.each(routes.map(({ path, title }) => [path.replace(':id', 'abc123'), title]))(
    '%s renders its placeholder page inside the shell',
    async (path, title) => {
      mockViewport(390)
      stubFetch(jsonResponse(200, ME))
      renderAt(path)
      const heading = await screen.findByRole('heading', { level: 1, name: title })
      expect(screen.getByRole('main')).toContainElement(heading)
      expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    },
  )
})

describe('App bootstrap', () => {
  it('shows the skeleton until /me loads, then the routes', async () => {
    mockViewport(390)
    const fetchMock = stubFetch(jsonResponse(200, ME))
    renderAt('/plan')

    expect(screen.getByRole('main', { name: 'Loading MealQuest' })).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()

    expect(await screen.findByRole('heading', { level: 1, name: 'Plan' })).toBeInTheDocument()
    expect(screen.queryByRole('main', { name: 'Loading MealQuest' })).not.toBeInTheDocument()
    expect(request(fetchMock, 0)).toMatchObject({ path: '/api/v1/me', method: 'GET', credentials: 'same-origin' })
  })

  it('sends a signed-out visitor to sign-in and never renders the routes', async () => {
    const fetchMock = stubFetch(errorResponse(401, 'unauthenticated'))
    const location = renderAt('/discover', fakeLocation('/discover', '?swipe=1'))

    await vi.waitFor(() => expect(location.assign).toHaveBeenCalledWith('/auth/login?return=%2Fdiscover%3Fswipe%3D1'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('main', { name: 'Loading MealQuest' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
  })

  it('shows the not-in-this-household screen on a 403', async () => {
    const fetchMock = stubFetch(errorResponse(403, 'forbidden'))
    const location = renderAt('/')

    expect(await screen.findByText(/not in this household/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Not in this household' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(location.assign).not.toHaveBeenCalled()
  })

  it('reports a /me failure with a retry instead of rendering the routes', async () => {
    mockViewport(390)
    stubFetch(errorResponse(500, 'internal'), jsonResponse(200, ME))
    renderAt('/')

    expect(await screen.findByRole('alert')).toHaveTextContent('MealQuest could not load: internal message')
    screen.getByRole('button', { name: 'Retry' }).click()
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
  })
})
