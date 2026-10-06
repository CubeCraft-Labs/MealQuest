import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { formatClockTime } from './api.ts'
import { SettingsPage } from './SettingsPage.tsx'

const HOUSEHOLD = { timezone: 'America/New_York', dinner_time: '17:30' }
const AI = { provider: 'anthropic', model: 'claude-sonnet-4-5' }

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubSettings(household = () => jsonResponse(200, HOUSEHOLD), ai = () => jsonResponse(200, AI)) {
  return stubFetchByPath({ '/api/v1/settings/household': household, '/api/v1/settings/ai': ai })
}

function CurrentPath() {
  return <p data-testid="path">{useLocation().pathname}</p>
}

function renderSettings() {
  const api = createApiClient(fakeLocation('/settings'), createNetworkStatus(window), createVerifiedSession(localStorage))
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter initialEntries={['/settings']}>
          <Routes>
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<CurrentPath />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

function row(name: string) {
  return screen.getByRole('link', { name: new RegExp(`^${name}`) })
}

describe('SettingsPage', () => {
  it('renders the title and the shared household banner', async () => {
    stubSettings()
    renderSettings()
    expect(screen.getByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(screen.getByText('Our kitchen')).toBeInTheDocument()
    expect(screen.getByText('Shared household · changes apply to everyone')).toBeInTheDocument()
    expect(await screen.findByText('Anthropic · claude-sonnet-4-5')).toBeInTheDocument()
  })

  it('groups the rows under Household, Alerts and Admin', async () => {
    stubSettings()
    renderSettings()
    const group = (name: string) => within(screen.getByRole('region', { name }))
    expect(group('Household').getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual([
      '/settings/regulars',
      '/settings/calendar',
      '/settings/household',
    ])
    expect(group('Alerts').getByRole('link')).toHaveAttribute('href', '/settings/notifications')
    expect(group('Admin').getByRole('link')).toHaveAttribute('href', '/settings/ai')
    await screen.findByText('Anthropic · claude-sonnet-4-5')
  })

  it('shows the live household and AI assistant status lines', async () => {
    const fetchMock = stubSettings()
    renderSettings()
    expect(await screen.findByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(within(row('AI assistant')).getByText('Anthropic · claude-sonnet-4-5')).toBeInTheDocument()
    expect(within(row('Household')).getByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([input]) => String(input)).sort()).toEqual([
      '/api/v1/settings/ai',
      '/api/v1/settings/household',
    ])
  })

  it('shows another provider and model as the settings name them', async () => {
    stubSettings(undefined, () => jsonResponse(200, { provider: 'openai_compatible', model: 'llama3.1:8b' }))
    renderSettings()
    expect(await screen.findByText('OpenAI-compatible · llama3.1:8b')).toBeInTheDocument()
  })

  it('shows no status line for rows the server has no endpoint for yet', async () => {
    stubSettings()
    renderSettings()
    await screen.findByText('Anthropic · claude-sonnet-4-5')
    expect(row('Regular dinners')).toHaveAccessibleName('Regular dinners')
    expect(row('Google Calendar')).toHaveAccessibleName('Google Calendar')
    expect(row('Notifications & Google Chat')).toHaveAccessibleName('Notifications & Google Chat')
  })

  it('marks the AI assistant row Admin and rings its card in grape-300', async () => {
    stubSettings()
    renderSettings()
    expect(within(row('AI assistant')).getByText('Admin')).toBeInTheDocument()
    const adminCard = screen.getByRole('region', { name: 'Admin' }).querySelector('ul')!.parentElement!
    expect(declaredStyle(adminCard).boxShadow).toBe('inset 0 0 0 2px var(--color-accent-300)')
    const householdCard = screen.getByRole('region', { name: 'Household' }).querySelector('ul')!.parentElement!
    expect(declaredStyle(householdCard).boxShadow).toBe('')
    await screen.findByText('Anthropic · claude-sonnet-4-5')
  })

  it.each([
    ['Regular dinners', '/settings/regulars'],
    ['Google Calendar', '/settings/calendar'],
    ['Household', '/settings/household'],
    ['Notifications & Google Chat', '/settings/notifications'],
    ['AI assistant', '/settings/ai'],
  ])('the %s row navigates to %s', async (name, path) => {
    stubSettings()
    renderSettings()
    await screen.findByText('Anthropic · claude-sonnet-4-5')
    fireEvent.click(row(name))
    expect(screen.getByTestId('path')).toHaveTextContent(path)
  })

  it('gives every row a tap target of at least 44 px', async () => {
    stubSettings()
    renderSettings()
    await screen.findByText('Anthropic · claude-sonnet-4-5')
    for (const link of screen.getAllByRole('link')) expectMinHitArea(link)
  })

  it('reports a failed status with a retry that loads it', async () => {
    let householdCalls = 0
    stubSettings(() => {
      householdCalls += 1
      return householdCalls === 1 ? errorResponse(500, 'internal') : jsonResponse(200, HOUSEHOLD)
    })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('Settings could not load: internal message')
    expect(screen.getByText('Anthropic · claude-sonnet-4-5')).toBeInTheDocument()
    expect(row('Household')).toHaveAccessibleName('Household')

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('refuses an AI provider it has no name for', async () => {
    stubSettings(undefined, () => jsonResponse(200, { provider: 'mystery', model: 'm1' }))
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown AI provider "mystery"')
    expect(row('AI assistant')).toHaveAccessibleName('AI assistant Admin')
  })

  it('refuses a dinner time that is not HH:MM', async () => {
    stubSettings(() => jsonResponse(200, { timezone: 'UTC', dinner_time: '5:30pm' }))
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('dinner_time "5:30pm" is not HH:MM')
  })
})

describe('formatClockTime', () => {
  it.each([
    ['00:05', '12:05 am'],
    ['09:00', '9:00 am'],
    ['12:00', '12:00 pm'],
    ['17:30', '5:30 pm'],
    ['23:59', '11:59 pm'],
  ])('formats %s as %s', (time, formatted) => {
    expect(formatClockTime(time)).toBe(formatted)
  })

  it('refuses a time that is not HH:MM', () => {
    expect(() => formatClockTime('24:00')).toThrow('not a 24-hour HH:MM time')
  })
})
