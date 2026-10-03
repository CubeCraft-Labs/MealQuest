import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { mockViewport } from './test/matchMedia.ts'
import App, { routes } from './App.tsx'

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
})

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
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
    (path, title) => {
      mockViewport(390)
      renderAt(path)
      expect(screen.getByRole('main')).toContainElement(screen.getByRole('heading', { level: 1, name: title }))
      expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    },
  )
})
