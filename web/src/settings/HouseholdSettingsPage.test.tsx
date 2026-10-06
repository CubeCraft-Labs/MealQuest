import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, ApiError, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { householdSettingsProblems } from './api.ts'
import { HouseholdSettingsPage } from './HouseholdSettingsPage.tsx'
import { matchesZone, utcOffset, zoneLabel } from './timezones.ts'

type Responder = (body: unknown) => Response | Promise<Response>

// stubAPI answers "METHOD path" from responders, each called with the
// request's decoded JSON body; GET /settings/household answers
// HOUSEHOLD_SETTINGS unless overridden. Any other request fails the test.
function stubAPI(responders: Record<string, Responder> = {}): Mock<typeof fetch> {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    ...responders,
  }
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

afterEach(() => {
  vi.unstubAllGlobals()
})

function CurrentPath() {
  return <p data-testid="path">{useLocation().pathname}</p>
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the page at /settings/household in a data router, so its leave guard works.
async function renderPage() {
  const api = createApiClient(
    fakeLocation('/settings/household'),
    createNetworkStatus(window),
    createVerifiedSession(localStorage),
  )
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings/household">
          <Routes>
            <Route path="/settings/household" element={<HouseholdSettingsPage />} />
            <Route path="*" element={<CurrentPath />} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
  await screen.findByLabelText('Dinner time')
}

const save = () => screen.getByRole('button', { name: 'Save changes' })
const input = (name: string) => screen.getByLabelText(name)
const zoneGroup = () => screen.getByRole('group', { name: 'Time zone' })

describe('HouseholdSettingsPage', () => {
  it('loads every household setting with plain-word copy', async () => {
    stubAPI()
    await renderPage()
    expect(screen.getByRole('button', { name: 'Back to Settings' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Household' })).toBeInTheDocument()
    expect(screen.getByText('Changes apply to everyone in the household.')).toBeInTheDocument()

    expect(zoneGroup()).toHaveTextContent('America / New York')
    expect(zoneGroup()).toHaveTextContent(utcOffset('America/New_York', new Date()))
    expect(input('Dinner time')).toHaveValue('17:30')
    expect(input('Dinner time')).toHaveAccessibleDescription(/crock-pot dinners count back from it/)
    expect(input('Day-of reminder')).toHaveValue('16:30')
    expect(input('Rotation check')).toHaveValue('09:00')
    expect(input('Rotation check')).toHaveAccessibleDescription(/When we ask whether to keep a new dinner/)
    expect(input('Plan draft day')).toHaveValue('5')
    expect(within(input('Plan draft day')).getByRole('option', { selected: true })).toHaveTextContent('Friday')
    expect(input('Plan draft time')).toHaveValue('09:00')
    expect(input('New suggestions day')).toHaveValue('0')
    expect(input('New suggestions time')).toHaveValue('08:00')
    expect(input('Google Chat Supply run day')).toHaveValue('6')
    expect(input('Google Chat Supply run time')).toHaveValue('08:00')
    expect(input('Google Chat Supply run time')).toHaveAccessibleDescription(/Supply run is sent to your Google Chat/)
    expect(save()).toBeDisabled()
  })

  it('reports a load failure with a retry', async () => {
    stubAPI({ 'GET /api/v1/settings/household': () => errorResponse(500, 'internal') })
    const api = createApiClient(
      fakeLocation('/settings/household'),
      createNetworkStatus(window),
      createVerifiedSession(localStorage),
    )
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/settings/household">
            <HouseholdSettingsPage />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )
    expect(await screen.findByRole('alert')).toHaveTextContent('Household settings could not load: internal message')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('saves only the edited fields', async () => {
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/household': (body) => jsonResponse(200, { ...HOUSEHOLD_SETTINGS, ...(body as object) }),
    })
    await renderPage()
    fireEvent.change(input('Dinner time'), { target: { value: '18:15' } })
    fireEvent.change(input('Rotation check'), { target: { value: '10:00' } })
    fireEvent.change(input('Plan draft day'), { target: { value: '4' } })
    fireEvent.change(input('Google Chat Supply run time'), { target: { value: '07:30' } })
    // Changed and changed back: not sent.
    fireEvent.change(input('Day-of reminder'), { target: { value: '16:00' } })
    fireEvent.change(input('Day-of reminder'), { target: { value: '16:30' } })
    fireEvent.click(save())

    expect(await screen.findByText('Changes saved.')).toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([
      {
        request: 'PATCH /api/v1/settings/household',
        body: { dinner_time: '18:15', review_time: '10:00', plan_draft_dow: 4, chat_autosend_time: '07:30' },
      },
    ])
    expect(input('Dinner time')).toHaveValue('18:15')
    expect(save()).toBeDisabled()
  })

  it('chooses a time zone from the searchable list', async () => {
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/household': (body) => jsonResponse(200, { ...HOUSEHOLD_SETTINGS, ...(body as object) }),
    })
    await renderPage()
    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    const sheet = screen.getByRole('dialog', { name: 'Choose a time zone' })
    const list = within(sheet).getByRole('list', { name: 'Time zones' })
    expect(within(list).getByRole('button', { pressed: true })).toHaveAccessibleName(
      `America / New York, ${utcOffset('America/New_York', new Date())}`,
    )

    fireEvent.change(within(sheet).getByRole('searchbox', { name: 'Search time zones' }), {
      target: { value: 'los angeles' },
    })
    const options = within(list).getAllByRole('button')
    expect(options).toHaveLength(1)
    expect(within(sheet).getByText('1 time zone')).toBeInTheDocument()
    expect(options[0]).toHaveAccessibleName(`America / Los Angeles, ${utcOffset('America/Los_Angeles', new Date())}`)
    fireEvent.click(options[0]!)

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(zoneGroup()).toHaveTextContent('America / Los Angeles')
    fireEvent.click(save())
    await screen.findByText('Changes saved.')
    expect(sent(fetchMock)).toEqual([
      { request: 'PATCH /api/v1/settings/household', body: { timezone: 'America/Los_Angeles' } },
    ])
  })

  it('offers the UTC zones the browser list leaves out', async () => {
    expect(Intl.supportedValuesOf('timeZone')).not.toContain('Etc/UTC')
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/household': (body) => jsonResponse(200, { ...HOUSEHOLD_SETTINGS, ...(body as object) }),
    })
    await renderPage()
    expect(zoneGroup()).toHaveTextContent('America / New York')
    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    const sheet = screen.getByRole('dialog', { name: 'Choose a time zone' })
    const list = within(sheet).getByRole('list', { name: 'Time zones' })
    fireEvent.change(within(sheet).getByRole('searchbox', { name: 'Search time zones' }), {
      target: { value: 'utc' },
    })
    expect(within(list).getByRole('button', { name: 'UTC, UTC+00:00' })).toBeInTheDocument()
    fireEvent.click(within(list).getByRole('button', { name: 'Etc / UTC, UTC+00:00' }))

    expect(zoneGroup()).toHaveTextContent('Etc / UTC')
    fireEvent.click(save())
    await screen.findByText('Changes saved.')
    expect(sent(fetchMock)).toEqual([
      { request: 'PATCH /api/v1/settings/household', body: { timezone: 'Etc/UTC' } },
    ])
  })

  it('keeps offering a saved zone the browser list leaves out after another is chosen', async () => {
    expect(Intl.supportedValuesOf('timeZone')).not.toContain('US/Eastern')
    stubAPI({
      'GET /api/v1/settings/household': () => jsonResponse(200, { ...HOUSEHOLD_SETTINGS, timezone: 'US/Eastern' }),
    })
    await renderPage()
    const search = (value: string) =>
      fireEvent.change(screen.getByRole('searchbox', { name: 'Search time zones' }), { target: { value } })
    const option = (name: RegExp) =>
      within(screen.getByRole('list', { name: 'Time zones' })).getByRole('button', { name })

    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    search('los angeles')
    fireEvent.click(option(/^America \/ Los Angeles,/))
    expect(zoneGroup()).toHaveTextContent('America / Los Angeles')

    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    search('us eastern')
    fireEvent.click(option(/^US \/ Eastern,/))
    expect(zoneGroup()).toHaveTextContent('US / Eastern')
    expect(save()).toBeDisabled()
  })

  it('says when no time zone matches the search', async () => {
    stubAPI()
    await renderPage()
    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    const sheet = screen.getByRole('dialog', { name: 'Choose a time zone' })
    fireEvent.change(within(sheet).getByRole('searchbox'), { target: { value: 'Atlantis' } })
    expect(within(sheet).getByText('No time zone matches.')).toBeInTheDocument()
    expect(within(sheet).queryAllByRole('listitem')).toHaveLength(0)
  })

  it('shows the API validation errors beside their fields', async () => {
    stubAPI({
      'PATCH /api/v1/settings/household': () =>
        jsonResponse(400, {
          error: {
            code: 'invalid_settings',
            message:
              'invalid household settings: timezone: unknown time zone "Mars/Olympus" (want an IANA zone such as America/New_York); dinner_time: invalid time "" (want 24-hour HH:MM, 00:00-23:59)',
            retryable: false,
          },
        }),
    })
    await renderPage()
    fireEvent.change(input('Dinner time'), { target: { value: '' } })
    fireEvent.click(save())

    expect(await screen.findByRole('alert')).toHaveTextContent('Not saved: fix the settings marked below their fields.')
    expect(input('Dinner time')).toHaveAttribute('aria-invalid', 'true')
    expect(input('Dinner time')).toHaveAccessibleDescription(/^invalid time "" \(want 24-hour HH:MM, 00:00-23:59\)/)
    expect(zoneGroup()).toHaveAccessibleDescription(
      'unknown time zone "Mars/Olympus" (want an IANA zone such as America/New_York)',
    )
    expect(input('Day-of reminder')).toHaveAttribute('aria-invalid', 'false')

    // Editing again clears the refused save's errors.
    fireEvent.change(input('Dinner time'), { target: { value: '18:00' } })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(input('Dinner time')).toHaveAttribute('aria-invalid', 'false')
  })

  it('shows an error that names no field as a whole-form error', async () => {
    stubAPI({ 'PATCH /api/v1/settings/household': () => errorResponse(500, 'internal') })
    await renderPage()
    fireEvent.change(input('Dinner time'), { target: { value: '18:00' } })
    fireEvent.click(save())
    expect(await screen.findByRole('alert')).toHaveTextContent('Not saved: internal message')
    expect(input('Dinner time')).toHaveValue('18:00')
    expect(save()).toBeEnabled()
  })

  it('asks before leaving with unsaved changes', async () => {
    stubAPI()
    await renderPage()
    fireEvent.change(input('New suggestions time'), { target: { value: '07:00' } })
    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    const confirm = await screen.findByRole('dialog', { name: 'Leave without saving?' })
    fireEvent.click(within(confirm).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(input('New suggestions time')).toHaveValue('07:00')

    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Leave' }))
    expect(await screen.findByTestId('path')).toHaveTextContent('/settings')
  })

  it('locks the form while a save is pending, and guards leaving until the save lands', async () => {
    let answer: (response: Response) => void = () => {}
    stubAPI({
      'PATCH /api/v1/settings/household': () => new Promise<Response>((resolve) => (answer = resolve)),
    })
    await renderPage()
    const unload = () => {
      const event = new Event('beforeunload', { cancelable: true })
      act(() => void window.dispatchEvent(event))
      return event.defaultPrevented
    }
    fireEvent.change(input('Dinner time'), { target: { value: '18:15' } })
    fireEvent.click(save())

    expect(await screen.findByRole('button', { name: 'Saving…' })).toBeDisabled()
    for (const field of ['Dinner time', 'Day-of reminder', 'Plan draft day', 'Google Chat Supply run time']) {
      expect(input(field)).toBeDisabled()
    }
    expect(screen.getByRole('button', { name: 'Change' })).toBeDisabled()
    expect(unload()).toBe(true)

    answer(jsonResponse(200, { ...HOUSEHOLD_SETTINGS, dinner_time: '18:15' }))
    expect(await screen.findByText('Changes saved.')).toBeInTheDocument()
    expect(input('Dinner time')).toBeEnabled()
    expect(input('Dinner time')).toHaveValue('18:15')
    await waitFor(() => expect(unload()).toBe(false))
  })

  it('leaves without asking when nothing changed', async () => {
    stubAPI()
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    expect(await screen.findByTestId('path')).toHaveTextContent('/settings')
  })

  it('keeps 44 px tap targets', async () => {
    stubAPI()
    await renderPage()
    for (const control of [
      screen.getByRole('button', { name: 'Back to Settings' }),
      within(zoneGroup()).getByRole('button', { name: 'Change' }),
      input('Dinner time'),
      input('Plan draft day'),
      input('Plan draft time'),
      save(),
    ]) {
      expectMinHitArea(control)
    }
    fireEvent.click(within(zoneGroup()).getByRole('button', { name: 'Change' }))
    expectMinHitArea(within(screen.getByRole('dialog')).getAllByRole('button', { pressed: true })[0]!)
  })
})

describe('householdSettingsProblems', () => {
  it('reads each field the server names', () => {
    const error = new ApiError(
      400,
      'invalid_settings',
      'invalid household settings: plan_draft_dow: invalid day of the week 9 (want 0-6, Sunday = 0); review_time: invalid time "9" (want 24-hour HH:MM, 00:00-23:59)',
      false,
    )
    expect(householdSettingsProblems(error)).toEqual({
      fields: {
        plan_draft_dow: 'invalid day of the week 9 (want 0-6, Sunday = 0)',
        review_time: 'invalid time "9" (want 24-hour HH:MM, 00:00-23:59)',
      },
      other: [],
    })
  })

  it('keeps a problem naming no field, and any other error, whole', () => {
    expect(
      householdSettingsProblems(
        new ApiError(400, 'invalid_settings', 'invalid household settings: settings: no settings to change', false),
      ),
    ).toEqual({ fields: {}, other: ['settings: no settings to change'] })
    expect(
      householdSettingsProblems(new ApiError(400, 'invalid_request', 'plan_draft_dow: want a JSON int', false)),
    ).toEqual({ fields: {}, other: ['plan_draft_dow: want a JSON int'] })
  })
})

describe('time zones', () => {
  it('formats the UTC offset', () => {
    const winter = new Date('2026-01-15T12:00:00Z')
    expect(utcOffset('America/New_York', winter)).toBe('UTC-05:00')
    expect(utcOffset('Asia/Kolkata', winter)).toBe('UTC+05:30')
    expect(utcOffset('UTC', winter)).toBe('UTC+00:00')
  })

  it('matches every search word against the name and offset', () => {
    const option = { zone: 'America/Argentina/Buenos_Aires', offset: 'UTC-03:00' }
    expect(zoneLabel(option.zone)).toBe('America / Argentina / Buenos Aires')
    expect(matchesZone(option, 'buenos aires')).toBe(true)
    expect(matchesZone(option, 'Buenos_Aires')).toBe(true)
    expect(matchesZone(option, 'utc-03')).toBe(true)
    expect(matchesZone(option, 'buenos paris')).toBe(false)
  })
})
