import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { GOOGLE_NOT_CONNECTED } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { CalendarSettingsPage } from './CalendarSettingsPage.tsx'

const GOOGLE = '/api/v1/integrations/google'
const CALENDARS = '/api/v1/integrations/google/calendars'
const CALENDAR = '/api/v1/integrations/google/calendar'

// CONNECTED is a connection pushing to the household's Dinners calendar.
const CONNECTED = {
  status: 'connected',
  calendar_id: 'dinners@group.calendar.google.com',
  calendar_name: 'Dinners',
  connected_at: '2026-10-07T15:00:00Z',
  last_sync_error: null,
}

// RECONNECT is CONNECTED once Google has refused its grant.
const RECONNECT = {
  ...CONNECTED,
  status: 'reconnect_required',
  last_sync_error: 'Google access was revoked or expired — reconnect',
}

// CALENDAR_LIST is the account's writable calendars, in Google's order.
const CALENDAR_LIST = [
  { id: 'sam@example.com', summary: 'sam@example.com', primary: true, accessRole: 'owner' },
  { id: 'dinners@group.calendar.google.com', summary: 'Dinners', primary: false, accessRole: 'owner' },
  { id: 'family@group.calendar.google.com', summary: 'Family', primary: false, accessRole: 'writer' },
]

type Responder = (body: unknown) => Response | Promise<Response>

// stubAPI answers "METHOD path" from responders, each called with the
// request's decoded JSON body. Any other request fails the test.
function stubAPI(responders: Record<string, Responder>): Mock<typeof fetch> {
  const all: Record<string, Responder> = { 'GET /api/v1/me': () => jsonResponse(200, ME), ...responders }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined
    return Promise.resolve(respond(body))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// sent lists the requests other than GETs, as "METHOD path" and body.
function sent(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([input, init]) => ({
      request: `${init?.method} ${String(input)}`,
      body: typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined,
    }))
}

// reads counts the GETs of path.
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

afterEach(() => {
  vi.unstubAllGlobals()
})

function CurrentPath() {
  return <p data-testid="path">{useLocation().pathname}</p>
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the page at /settings/calendar in a data router.
async function renderPage() {
  const api = createApiClient(fakeLocation('/settings/calendar'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const queryClient = createQueryClient()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <InDataRouter path="/settings/calendar">
          <Routes>
            <Route path="/settings/calendar" element={<CalendarSettingsPage />} />
            <Route path="*" element={<CurrentPath />} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
  return queryClient
}

describe('CalendarSettingsPage', () => {
  it('shows the screen title and goes back to Settings', async () => {
    stubAPI({ [`GET ${GOOGLE}`]: () => jsonResponse(200, GOOGLE_NOT_CONNECTED) })
    await renderPage()

    expect(screen.getByRole('heading', { level: 1, name: 'Google Calendar' })).toBeInTheDocument()
    expect(screen.getByText('Settings')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    expect(await screen.findByTestId('path')).toHaveTextContent('/settings')
  })

  it('shows "Connect Google Calendar" when not connected, opening the connect endpoint as a page', async () => {
    const fetchMock = stubAPI({ [`GET ${GOOGLE}`]: () => jsonResponse(200, GOOGLE_NOT_CONNECTED) })
    await renderPage()

    const connect = await screen.findByRole('link', { name: 'Connect Google Calendar' })
    expect(connect).toHaveAttribute('href', '/api/v1/integrations/google/connect')
    expect(declaredStyle(connect).background).toBe('var(--color-accent)')
    expect(screen.getByRole('region', { name: 'Not connected' })).toBeInTheDocument()
    // Nothing to pick yet, nor to disconnect, and Google is not asked.
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Disconnect' })).not.toBeInTheDocument()
    expect(reads(fetchMock, CALENDARS)).toBe(0)
  })

  it('reports a status that could not load, and reads it again on Retry', async () => {
    let status = () => errorResponse(500, 'internal')
    stubAPI({ [`GET ${GOOGLE}`]: () => status() })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Google Calendar status could not load: internal message')
    status = () => jsonResponse(200, GOOGLE_NOT_CONNECTED)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('link', { name: 'Connect Google Calendar' })).toBeInTheDocument()
  })

  it('refuses a status it does not know', async () => {
    stubAPI({ [`GET ${GOOGLE}`]: () => jsonResponse(200, { ...GOOGLE_NOT_CONNECTED, status: 'paused' }) })
    await renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent('response is not a Google Calendar connection')
  })

  it('shows "Connected · {calendar}" in teal-800 and the writable calendars to pick from', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
    })
    await renderPage()

    const connected = await screen.findByText('Connected · Dinners')
    expect(declaredStyle(connected).color).toBe('var(--color-accent-2-800)')
    expect(connected.querySelector('svg')).not.toBeNull()
    const picker = await screen.findByRole('combobox', { name: 'Calendar' })
    expect(picker).toHaveValue('dinners@group.calendar.google.com')
    expect(within(picker).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'sam@example.com (primary)',
      'Dinners',
      'Family',
    ])
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Connect|Reconnect/ })).not.toBeInTheDocument()
  })

  it('chooses another calendar at once, showing it saving, and reads the plans again', async () => {
    const answer = deferred()
    const fetchMock = stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
      [`PUT ${CALENDAR}`]: () => answer.response,
    })
    const queryClient = await renderPage()
    queryClient.setQueryData(['plans', '2026-10-05'], null)

    const picker = await screen.findByRole('combobox', { name: 'Calendar' })
    fireEvent.change(picker, { target: { value: 'family@group.calendar.google.com' } })
    await waitFor(() =>
      expect(sent(fetchMock)).toEqual([{ request: `PUT ${CALENDAR}`, body: { calendar_id: 'family@group.calendar.google.com' } }]),
    )
    expect(await screen.findByText('Saving…')).toBeInTheDocument()
    expect(picker).toBeDisabled()
    expect(picker).toHaveValue('family@group.calendar.google.com')

    answer.send(jsonResponse(200, { calendar_id: 'family@group.calendar.google.com', calendar_name: 'Family' }))
    expect(await screen.findByText('Connected · Family')).toBeInTheDocument()
    expect(picker).toBeEnabled()
    expect(picker).toHaveValue('family@group.calendar.google.com')
    expect(screen.queryByText('Saving…')).not.toBeInTheDocument()
    // Choosing another calendar makes every pushed plan changed.
    expect(queryClient.getQueryState(['plans', '2026-10-05'])?.isInvalidated).toBe(true)
  })

  it('says a choice that was not saved, keeping the calendar chosen before', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
      [`PUT ${CALENDAR}`]: () => errorResponse(422, 'unknown_calendar'),
    })
    await renderPage()

    const picker = await screen.findByRole('combobox', { name: 'Calendar' })
    fireEvent.change(picker, { target: { value: 'family@group.calendar.google.com' } })
    expect(await screen.findByRole('alert')).toHaveTextContent('Not saved: unknown_calendar message')
    expect(picker).toHaveValue('dinners@group.calendar.google.com')
    expect(screen.getByText('Connected · Dinners')).toBeInTheDocument()
  })

  it('asks to choose a calendar when connected with none chosen', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, { ...CONNECTED, calendar_id: null, calendar_name: null }),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
    })
    await renderPage()

    expect(await screen.findByText('Connected')).toBeInTheDocument()
    expect(await screen.findByText('Choose the calendar to push dinners to.')).toBeInTheDocument()
    const picker = screen.getByRole('combobox', { name: 'Calendar' })
    expect(picker).toHaveValue('')
    expect(within(picker).getByRole('option', { name: 'Choose a calendar' })).toBeDisabled()
  })

  it('says the chosen calendar can no longer be pushed to once it drops out of the list', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST.filter((calendar) => calendar.summary !== 'Dinners')),
    })
    await renderPage()

    expect(
      await screen.findByText('Dinners was deleted or MealQuest can no longer add dinners to it. Choose another calendar.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Calendar' })).toHaveValue('')
  })

  it('says when the account has no calendar MealQuest can add dinners to', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, []),
    })
    await renderPage()

    expect(await screen.findByText('This Google account has no calendar MealQuest can add dinners to.')).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('reports calendars that could not load, and lists them again on Retry', async () => {
    let list = () => errorResponse(502, 'google_failed')
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => list(),
    })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Your calendars could not load: google_failed message')
    list = () => jsonResponse(200, CALENDAR_LIST)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('combobox', { name: 'Calendar' })).toHaveValue('dinners@group.calendar.google.com')
  })

  it('turns to the reconnect banner when Google refuses the grant while listing calendars', async () => {
    let status: unknown = CONNECTED
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, status),
      [`GET ${CALENDARS}`]: () => {
        status = RECONNECT
        return errorResponse(409, 'google_reconnect_required')
      },
    })
    await renderPage()

    const banner = await screen.findByRole('region', { name: 'Reconnect Google Calendar' })
    // Why and what to do are announced as the screen turns to the banner.
    const alert = screen.getByRole('alert')
    expect(banner).toContainElement(alert)
    expect(alert).toHaveTextContent(
      'Google access was revoked or expired — reconnect' + 'Nothing can be pushed to Calendar until you connect again.',
    )
    expect(within(alert).queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByText('Connected · Dinners')).not.toBeInTheDocument()
  })

  it('shows a grape banner with Reconnect when reconnect is required, and offers nothing that needs Google', async () => {
    const fetchMock = stubAPI({ [`GET ${GOOGLE}`]: () => jsonResponse(200, RECONNECT) })
    await renderPage()

    const banner = await screen.findByRole('region', { name: 'Reconnect Google Calendar' })
    expect(declaredStyle(banner).background).toBe('var(--color-accent-100)')
    expect(declaredStyle(banner).color).toBe('var(--color-accent-800)')
    expect(banner).toHaveTextContent('Google access was revoked or expired — reconnect')
    expect(banner).toHaveTextContent('Nothing can be pushed to Calendar until you connect again.')
    expect(within(banner).getByRole('link', { name: 'Reconnect' })).toHaveAttribute(
      'href',
      '/api/v1/integrations/google/connect',
    )
    // The chosen calendar is kept, but not picked from, nor called connected.
    expect(screen.getByText('Dinners go to Dinners once Google Calendar is reconnected.')).toBeInTheDocument()
    expect(screen.queryByText(/^Connected/)).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(reads(fetchMock, CALENDARS)).toBe(0)
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeInTheDocument()
  })

  it('disconnects only once confirmed, then shows Connect Google Calendar', async () => {
    let status: unknown = CONNECTED
    const answer = deferred()
    const fetchMock = stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, status),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
      [`DELETE ${GOOGLE}`]: () => answer.response,
    })
    const queryClient = await renderPage()
    queryClient.setQueryData(['plans', '2026-10-05'], null)

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }))
    let dialog = await screen.findByRole('dialog', { name: 'Disconnect Google Calendar?' })
    expect(dialog).toHaveTextContent('Dinners already pushed to Dinners stay on the calendar.')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep connected' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(sent(fetchMock)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
    dialog = await screen.findByRole('dialog', { name: 'Disconnect Google Calendar?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Disconnect' }))
    await waitFor(() => expect(sent(fetchMock)).toEqual([{ request: `DELETE ${GOOGLE}`, body: undefined }]))
    // While it disconnects the sheet stays, and cannot be dismissed.
    expect(await within(dialog).findByRole('button', { name: 'Disconnecting…' })).toBeDisabled()
    expect(within(dialog).getByRole('button', { name: 'Keep connected' })).toBeDisabled()

    status = GOOGLE_NOT_CONNECTED
    answer.send(new Response(null, { status: 204 }))
    expect(await screen.findByRole('link', { name: 'Connect Google Calendar' })).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    // Disconnecting forgets every pushed event, so pushed plans are changed.
    expect(queryClient.getQueryState(['plans', '2026-10-05'])?.isInvalidated).toBe(true)
  })

  it('says a disconnect Google refused, keeping the connection', async () => {
    stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, CONNECTED),
      [`GET ${CALENDARS}`]: () => jsonResponse(200, CALENDAR_LIST),
      [`DELETE ${GOOGLE}`]: () => errorResponse(502, 'revoke_failed'),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }))
    const dialog = await screen.findByRole('dialog', { name: 'Disconnect Google Calendar?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Disconnect' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Not disconnected: revoke_failed message')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep connected' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByText('Connected · Dinners')).toBeInTheDocument()
  })

  it('disconnects a connection that needs reconnecting', async () => {
    let status: unknown = RECONNECT
    const fetchMock = stubAPI({
      [`GET ${GOOGLE}`]: () => jsonResponse(200, status),
      [`DELETE ${GOOGLE}`]: () => {
        status = GOOGLE_NOT_CONNECTED
        return new Response(null, { status: 204 })
      },
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }))
    const dialog = await screen.findByRole('dialog', { name: 'Disconnect Google Calendar?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Disconnect' }))
    expect(await screen.findByRole('link', { name: 'Connect Google Calendar' })).toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([{ request: `DELETE ${GOOGLE}`, body: undefined }])
  })
})
