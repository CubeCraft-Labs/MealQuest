import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { CHAT_WEBHOOK, CHAT_WEBHOOK_NONE, NOTIFICATION_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { PUSH_ENDPOINT, restorePushEnvironment, stubPushEnvironment, VAPID_PUBLIC_KEY } from '../test/push.ts'
import { NotificationsPage } from './NotificationsPage.tsx'

type Responder = (body: unknown) => Response

// stubAPI answers "METHOD path" from responders, each called with the
// request's decoded JSON body; the settings the page loads answer their
// fixtures unless overridden. Any other request fails the test.
function stubAPI(responders: Record<string, Responder> = {}): Mock<typeof fetch> {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/notifications': () => jsonResponse(200, NOTIFICATION_SETTINGS),
    'GET /api/v1/settings/chat-webhook': () => jsonResponse(200, CHAT_WEBHOOK),
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

// bodiesOf is the decoded JSON body of every "METHOD path" request sent.
function bodiesOf(fetchMock: Mock<typeof fetch>, method: string, path: string): unknown[] {
  return fetchMock.mock.calls
    .filter(([input, init]) => String(input) === path && (init?.method ?? 'GET') === method)
    .map(([, init]) => JSON.parse(String(init?.body)) as unknown)
}

afterEach(() => {
  vi.unstubAllGlobals()
  restorePushEnvironment()
})

async function renderPage() {
  const api = createApiClient(
    fakeLocation('/settings/notifications'),
    createNetworkStatus(window),
    createVerifiedSession(localStorage),
  )
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings/notifications">
          <NotificationsPage />
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

const toggle = (name: string) => screen.getByRole('switch', { name })

// withChange is the seed notification settings with one change.
function withChange(event: string, channel: 'push' | 'chat', on: boolean) {
  return {
    ...NOTIFICATION_SETTINGS,
    prefs: NOTIFICATION_SETTINGS.prefs.map((p) => (p.event === event ? { ...p, [channel]: on } : p)),
  }
}

describe('NotificationsPage (mock 1n)', () => {
  it('shows the back button, the title and the five events in order with their channels', async () => {
    stubAPI()
    await renderPage()
    expect(screen.getByRole('button', { name: 'Back to Settings' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Notifications' })).toBeInTheDocument()

    const matrix = await screen.findByRole('group', { name: 'Notification channels' })
    expect(
      Array.from(matrix.children)
        .slice(0, 3)
        .map((cell) => cell.textContent),
    ).toEqual(['Event', 'Push', 'Chat'])
    const switches = within(matrix).getAllByRole('switch')
    expect(switches.map((s) => s.getAttribute('aria-label'))).toEqual([
      'New weekly plan ready: Push',
      'New weekly plan ready: Google Chat',
      'Shopping list ready: Push',
      'Shopping list ready: Google Chat',
      'Day-of dinner reminder: Push',
      'Day-of dinner reminder: Google Chat',
      'New suggestions to swipe: Push',
      'New suggestions to swipe: Google Chat',
      'Keep it in rotation?: Push',
      'Keep it in rotation?: Google Chat',
    ])
    expect(switches.map((s) => s.getAttribute('aria-checked'))).toEqual([
      'true', 'true', 'true', 'true', 'true', 'false', 'true', 'false', 'true', 'false',
    ])
    switches.forEach(expectMinHitArea)
    const time = within(matrix).getByRole('button', { name: 'Reminder time, 4:30 pm. Change' })
    expect(time).toHaveTextContent('at 4:30 pm')
    expectMinHitArea(time)
  })

  it('saves a toggled channel with every other event unchanged', async () => {
    const fetchMock = stubAPI({
      'PUT /api/v1/settings/notifications': (body) => jsonResponse(200, body),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'Keep it in rotation?: Google Chat' }))

    await waitFor(() => expect(toggle('Keep it in rotation?: Google Chat')).toHaveAttribute('aria-checked', 'true'))
    expect(bodiesOf(fetchMock, 'PUT', '/api/v1/settings/notifications')).toEqual([
      withChange('rotation_review', 'chat', true),
    ])
    await waitFor(() => expect(toggle('Keep it in rotation?: Google Chat')).toBeEnabled())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the stored channels and why when a save fails, and retries it', async () => {
    let fail = true
    const fetchMock = stubAPI({
      'PUT /api/v1/settings/notifications': (body) =>
        fail ? errorResponse(500, 'internal') : jsonResponse(200, body),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'New weekly plan ready: Push' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Notification settings were not saved')
    expect(toggle('New weekly plan ready: Push')).toHaveAttribute('aria-checked', 'true')

    fail = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(toggle('New weekly plan ready: Push')).toHaveAttribute('aria-checked', 'false'))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(bodiesOf(fetchMock, 'PUT', '/api/v1/settings/notifications')).toEqual([
      withChange('plan_ready', 'push', false),
      withChange('plan_ready', 'push', false),
    ])
  })

  it('edits the reminder time in a sheet', async () => {
    const fetchMock = stubAPI({
      'PUT /api/v1/settings/notifications': (body) => jsonResponse(200, body),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Reminder time, 4:30 pm. Change' }))

    const sheet = screen.getByRole('dialog', { name: 'Day-of dinner reminder' })
    const input = within(sheet).getByLabelText('Reminder time')
    expect(input).toHaveValue('16:30')
    expect(within(sheet).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(input, { target: { value: '18:15' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Reminder time, 6:15 pm. Change' })).toHaveTextContent('at 6:15 pm')
    expect(bodiesOf(fetchMock, 'PUT', '/api/v1/settings/notifications')).toEqual([
      { ...NOTIFICATION_SETTINGS, reminder_time: '18:15' },
    ])
  })

  it('keeps the reminder sheet open and says why when the time is refused', async () => {
    stubAPI({
      'PUT /api/v1/settings/notifications': () => errorResponse(400, 'invalid_settings'),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Reminder time, 4:30 pm. Change' }))
    const sheet = screen.getByRole('dialog', { name: 'Day-of dinner reminder' })
    fireEvent.change(within(sheet).getByLabelText('Reminder time'), { target: { value: '07:00' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save' }))

    expect(await within(sheet).findByRole('alert')).toHaveTextContent('The reminder time was not saved')
    expect(screen.getByRole('button', { name: 'Reminder time, 4:30 pm. Change' })).toBeInTheDocument()
  })

  it('shows the preview bubble', async () => {
    stubAPI()
    await renderPage()
    const preview = screen.getByRole('figure', { name: 'Google Chat message preview' })
    expect(preview).toHaveTextContent('MealQuest · preview')
    expect(preview).toHaveTextContent(
      "This week's plan is ready: Tacos, Bolognese, Tikka masala + 4 more. Open the plan →",
    )
  })
})

describe('NotificationsPage Google Chat webhook', () => {
  it('shows the masked URL and the connected space', async () => {
    stubAPI()
    await renderPage()
    const card = screen.getByRole('region', { name: 'Google Chat webhook' })
    expect(await within(card).findByRole('textbox', { name: 'Webhook URL' })).toHaveValue(CHAT_WEBHOOK.url_masked)
    expect(within(card).getByRole('textbox', { name: 'Webhook URL' })).toHaveAttribute('readonly')
    expect(within(card).getByText('Connected · Dinner Crew space')).toBeInTheDocument()
  })

  it.each([
    ['success', () => jsonResponse(200, { http_status: 200, ...CHAT_WEBHOOK, last_test_status: 'ok' }), 'Test message sent.'],
    [
      'the HTTP status Google Chat refused it with',
      () => jsonResponse(200, { http_status: 403, ...CHAT_WEBHOOK, last_test_status: 'failed' }),
      'Google Chat refused the test message: HTTP 403.',
    ],
    [
      'why it was not sent',
      () => errorResponse(502, 'chat_unreachable'),
      'Test message not sent',
    ],
  ])('shows %s under the field after Send test', async (_, respond, said) => {
    const fetchMock = stubAPI({ 'POST /api/v1/settings/chat-webhook/test': respond })
    await renderPage()
    const card = screen.getByRole('region', { name: 'Google Chat webhook' })
    fireEvent.click(await within(card).findByRole('button', { name: 'Send test' }))

    const result = await within(card).findByText(said, { exact: false })
    // Under the field: the result follows the URL input.
    const input = within(card).getByRole('textbox', { name: 'Webhook URL' })
    expect(input.compareDocumentPosition(result) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/settings/chat-webhook/test')).toHaveLength(1)
  })

  it('asks for a webhook when none is stored and saves it', async () => {
    const fetchMock = stubAPI({
      'GET /api/v1/settings/chat-webhook': () => jsonResponse(200, CHAT_WEBHOOK_NONE),
      'PUT /api/v1/settings/chat-webhook': () => jsonResponse(200, CHAT_WEBHOOK),
    })
    await renderPage()
    const card = screen.getByRole('region', { name: 'Google Chat webhook' })
    const save = await within(card).findByRole('button', { name: 'Save webhook' })
    expect(save).toBeDisabled()
    fireEvent.change(within(card).getByRole('textbox', { name: 'Webhook URL' }), {
      target: { value: ' https://chat.googleapis.com/v1/spaces/AAQA/messages?key=k&token=t ' },
    })
    fireEvent.change(within(card).getByRole('textbox', { name: 'Space name' }), { target: { value: 'Dinner Crew space' } })
    fireEvent.click(save)

    expect(await within(card).findByText('Connected · Dinner Crew space')).toBeInTheDocument()
    expect(bodiesOf(fetchMock, 'PUT', '/api/v1/settings/chat-webhook')).toEqual([
      { url: 'https://chat.googleapis.com/v1/spaces/AAQA/messages?key=k&token=t', space_label: 'Dinner Crew space' },
    ])
  })
})

describe('NotificationsPage push permission row', () => {
  const row = () => screen.getByRole('group', { name: 'Notifications on this device' })

  it('says a browser without Web Push is not supported, with no button', async () => {
    stubAPI()
    await renderPage()
    expect(await within(row()).findByText('Not supported')).toBeInTheDocument()
    expect(within(row()).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText("This browser can't receive notifications.")).toBeInTheDocument()
  })

  it('turns push on from the tap: asks permission, subscribes and stores the subscription', async () => {
    const browser = stubPushEnvironment({ permission: 'default', answer: 'granted' })
    const fetchMock = stubAPI({
      'GET /api/v1/push/vapid-public-key': () => jsonResponse(200, VAPID_PUBLIC_KEY),
      'POST /api/v1/push/subscriptions': () => new Response(null, { status: 201 }),
    })
    await renderPage()
    expect(await within(row()).findByText('Off')).toBeInTheDocument()
    fireEvent.click(within(row()).getByRole('button', { name: 'Turn on' }))

    expect(await within(row()).findByText('On')).toBeInTheDocument()
    expect(browser.notification.requestPermission).toHaveBeenCalledTimes(1)
    expect(browser.pushManager.subscribe).toHaveBeenCalledTimes(1)
    const [stored] = bodiesOf(fetchMock, 'POST', '/api/v1/push/subscriptions')
    expect(stored).toMatchObject({ endpoint: PUSH_ENDPOINT })
    expect(within(row()).getByRole('button', { name: 'Turn off' })).toBeInTheDocument()
  })

  it('says the prompt was dismissed and push is still off', async () => {
    stubPushEnvironment({ permission: 'default', answer: 'default' })
    stubAPI()
    await renderPage()
    fireEvent.click(await within(row()).findByRole('button', { name: 'Turn on' }))
    expect(await screen.findByText(/closed without an answer/)).toBeInTheDocument()
    expect(within(row()).getByText('Off')).toBeInTheDocument()
  })

  it('shows Blocked once the browser denies permission, with no button', async () => {
    stubPushEnvironment({ permission: 'default', answer: 'denied' })
    stubAPI()
    await renderPage()
    fireEvent.click(await within(row()).findByRole('button', { name: 'Turn on' }))
    expect(await within(row()).findByText('Blocked')).toBeInTheDocument()
    expect(within(row()).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText(/blocks notifications from MealQuest/)).toBeInTheDocument()
  })

  it('shows why turning on failed', async () => {
    stubPushEnvironment({ permission: 'granted' })
    stubAPI({ 'GET /api/v1/push/vapid-public-key': () => errorResponse(503, 'push_unavailable') })
    await renderPage()
    fireEvent.click(await within(row()).findByRole('button', { name: 'Turn on' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Notifications could not be turned on')
    expect(within(row()).getByText('Off')).toBeInTheDocument()
  })

  it('turns push off on this device', async () => {
    stubPushEnvironment({ permission: 'granted', subscribed: true })
    const fetchMock = stubAPI({ 'DELETE /api/v1/push/subscriptions': () => new Response(null, { status: 204 }) })
    await renderPage()
    fireEvent.click(await within(row()).findByRole('button', { name: 'Turn off' }))
    expect(await within(row()).findByText('Off')).toBeInTheDocument()
    expect(bodiesOf(fetchMock, 'DELETE', '/api/v1/push/subscriptions')).toEqual([{ endpoint: PUSH_ENDPOINT }])
  })
})
