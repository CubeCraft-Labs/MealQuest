import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { AI_SETTINGS, HOUSEHOLD_SETTINGS, SETTINGS_API } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { formatClockTime, modelDisplayName } from './api.ts'
import { SettingsPage } from './SettingsPage.tsx'

const HOUSEHOLD = HOUSEHOLD_SETTINGS
const AI = AI_SETTINGS

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

const REGULARS_PATH = '/api/v1/dinners?status=regular'
const CALENDAR_PATH = '/api/v1/settings/calendar'
const NOTIFICATIONS_PATH = '/api/v1/settings/notifications'

// stubSettings answers as today's server does (see SETTINGS_API), with
// overrides for the paths a test changes.
function stubSettings(overrides: Record<string, () => Response> = {}) {
  return stubFetchByPath({
    ...SETTINGS_API,
    '/api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD),
    '/api/v1/settings/ai': () => jsonResponse(200, AI),
    ...overrides,
  })
}

function CurrentPath() {
  return <p data-testid="path">{useLocation().pathname}</p>
}

// renderSettings renders the settings home on a 390 px phone (mock 1p);
// SettingsTabs.test.tsx covers 768 px and up.
function renderSettings() {
  mockViewport(390)
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
    expect(await screen.findByText('Anthropic · Claude Sonnet')).toBeInTheDocument()
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
    await screen.findByText('Anthropic · Claude Sonnet')
  })

  it('reads every status line from the server', async () => {
    const fetchMock = stubSettings()
    renderSettings()
    expect(await screen.findByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(within(row('AI assistant')).getByText('Anthropic · Claude Sonnet')).toBeInTheDocument()
    expect(within(row('Household')).getByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([input]) => String(input)).sort()).toEqual([
      REGULARS_PATH,
      '/api/v1/settings/ai',
      CALENDAR_PATH,
      '/api/v1/settings/household',
      NOTIFICATIONS_PATH,
    ])
  })

  it('shows the not-yet-available states while the server has no endpoint for them', async () => {
    stubSettings()
    renderSettings()
    expect(await within(row('Regular dinners')).findByText('Rotation count unavailable')).toBeInTheDocument()
    expect(await within(row('Google Calendar')).findByText('Not connected')).toBeInTheDocument()
    expect(
      await within(row('Notifications & Google Chat')).findByText('Push off · Google Chat not connected'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the Regulars on rotation, the connected calendar and the notifications once served', async () => {
    stubSettings({
      [REGULARS_PATH]: () => jsonResponse(200, { dinners: Array.from({ length: 7 }, (_, i) => ({ id: `d${i}` })) }),
      [CALENDAR_PATH]: () => jsonResponse(200, { connected: true, calendar_name: 'Dinners calendar' }),
      [NOTIFICATIONS_PATH]: () => jsonResponse(200, { push: true, chat_space: 'Dinner Crew space' }),
    })
    renderSettings()
    expect(await within(row('Regular dinners')).findByText('7 on rotation')).toBeInTheDocument()
    expect(await within(row('Google Calendar')).findByText('Connected · Dinners calendar')).toBeInTheDocument()
    expect(
      await within(row('Notifications & Google Chat')).findByText('Push on · Dinner Crew space'),
    ).toBeInTheDocument()
  })

  it('shows no Regulars, a disconnected calendar and push off with no Google Chat space', async () => {
    stubSettings({
      [REGULARS_PATH]: () => jsonResponse(200, { dinners: [] }),
      [CALENDAR_PATH]: () => jsonResponse(200, { connected: false }),
      [NOTIFICATIONS_PATH]: () => jsonResponse(200, { push: false, chat_space: null }),
    })
    renderSettings()
    expect(await within(row('Regular dinners')).findByText('None on rotation yet')).toBeInTheDocument()
    expect(await within(row('Google Calendar')).findByText('Not connected')).toBeInTheDocument()
    expect(
      await within(row('Notifications & Google Chat')).findByText('Push off · Google Chat not connected'),
    ).toBeInTheDocument()
  })

  it('reports a status endpoint failure other than a missing endpoint', async () => {
    stubSettings({ [CALENDAR_PATH]: () => errorResponse(500, 'internal') })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('Settings could not load: internal message')
    expect(row('Google Calendar')).toHaveAccessibleName('Google Calendar')
  })

  it('refuses a connected calendar without a name', async () => {
    stubSettings({ [CALENDAR_PATH]: () => jsonResponse(200, { connected: true }) })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('response is not a calendar status')
  })

  it('shows another provider and model as the settings name them', async () => {
    stubSettings({
      '/api/v1/settings/ai': () => jsonResponse(200, { ...AI, provider: 'openai_compatible', model: 'llama3.1:8b' }),
    })
    renderSettings()
    expect(await screen.findByText('OpenAI-compatible · llama3.1:8b')).toBeInTheDocument()
  })

  it('says no model is chosen for the seeded AI settings', async () => {
    stubSettings({ '/api/v1/settings/ai': () => jsonResponse(200, { ...AI, model: '' }) })
    renderSettings()
    expect(await within(row('AI assistant')).findByText('Anthropic · No model chosen')).toBeInTheDocument()
  })

  it('marks the AI assistant row Admin and rings its card in grape-300', async () => {
    stubSettings()
    renderSettings()
    expect(within(row('AI assistant')).getByText('Admin')).toBeInTheDocument()
    const adminCard = screen.getByRole('region', { name: 'Admin' }).querySelector('ul')!.parentElement!
    expect(declaredStyle(adminCard).boxShadow).toBe('inset 0 0 0 2px var(--color-accent-300)')
    const householdCard = screen.getByRole('region', { name: 'Household' }).querySelector('ul')!.parentElement!
    expect(declaredStyle(householdCard).boxShadow).toBe('')
    await screen.findByText('Anthropic · Claude Sonnet')
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
    await screen.findByText('Anthropic · Claude Sonnet')
    fireEvent.click(row(name))
    expect(screen.getByTestId('path')).toHaveTextContent(path)
  })

  it('gives every row a tap target of at least 44 px', async () => {
    stubSettings()
    renderSettings()
    await screen.findByText('Anthropic · Claude Sonnet')
    for (const link of screen.getAllByRole('link')) expectMinHitArea(link)
  })

  it('reports a failed status with a retry that loads it', async () => {
    let householdCalls = 0
    stubSettings({
      '/api/v1/settings/household': () => {
        householdCalls += 1
        return householdCalls === 1 ? errorResponse(500, 'internal') : jsonResponse(200, HOUSEHOLD)
      },
    })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('Settings could not load: internal message')
    expect(screen.getByText('Anthropic · Claude Sonnet')).toBeInTheDocument()
    expect(row('Household')).toHaveAccessibleName('Household')

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('America/New_York · Dinner at 5:30 pm')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('refuses an AI provider it has no name for', async () => {
    stubSettings({ '/api/v1/settings/ai': () => jsonResponse(200, { ...AI, provider: 'mystery', model: 'm1' }) })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown AI provider "mystery"')
    expect(row('AI assistant')).toHaveAccessibleName('AI assistant Admin')
  })

  it('refuses a dinner time that is not HH:MM', async () => {
    stubSettings({
      '/api/v1/settings/household': () => jsonResponse(200, { ...HOUSEHOLD, timezone: 'UTC', dinner_time: '5:30pm' }),
    })
    renderSettings()
    expect(await screen.findByRole('alert')).toHaveTextContent('dinner_time "5:30pm" is not HH:MM')
  })
})

describe('modelDisplayName', () => {
  it.each([
    ['claude-sonnet-4-5', 'Claude Sonnet'],
    ['claude-opus-4-1-20250805', 'Claude Opus'],
    ['claude-3-5-haiku-20241022', 'Claude Haiku'],
    ['claude-fable-5-1', 'Claude Fable'],
    ['gpt-4o', 'gpt-4o'],
    ['anthropic/claude-sonnet-4.5', 'anthropic/claude-sonnet-4.5'],
  ])('names %s %s', (model, name) => {
    expect(modelDisplayName(model)).toBe(name)
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
