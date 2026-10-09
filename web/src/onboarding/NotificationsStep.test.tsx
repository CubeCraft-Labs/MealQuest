import { fireEvent, render, screen, within } from '@testing-library/react'
import { Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { createApiClient, createVerifiedSession } from '../api/index.ts'
import App from '../App.tsx'
import { AuthBootstrap } from '../AuthBootstrap.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { ApiProviders, HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { PUSH_ENDPOINT, restorePushEnvironment, stubPushEnvironment, VAPID_PUBLIC_KEY } from '../test/push.ts'
import { NotificationsStep } from './NotificationsStep.tsx'
import styles from './Onboarding.module.css'

const IPHONE_SAFARI =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1'

const COMPLETED_AT = '2026-10-06T21:30:00.000Z'

// requestPermission stands in for the browser's Notification API, which
// jsdom lacks, so a test can prove nothing asks for permission.
let requestPermission: Mock<() => Promise<NotificationPermission>>

beforeEach(() => {
  requestPermission = vi.fn<() => Promise<NotificationPermission>>(() => Promise.resolve('granted'))
  vi.stubGlobal('Notification', { permission: 'default', requestPermission })
})

afterEach(() => {
  restorePushEnvironment()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(navigator, 'maxTouchPoints')
  Reflect.deleteProperty(navigator, 'standalone')
  localStorage.clear()
})

// A PATCH the fake server holds until the test answers it.
type Held = { body: unknown; answer: (response: Response) => void }

// stubFirstRunServer stands in for the server on first run: signed in,
// onboarding not finished. Each PATCH of the household settings is held in
// `held` until the test answers it. It hands out the VAPID public key with
// vapidKey and stores push subscriptions in `subscriptions`.
function stubFirstRunServer(vapidKey: () => Response = () => jsonResponse(200, VAPID_PUBLIC_KEY)) {
  const held: Held[] = []
  const subscriptions: unknown[] = []
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const path = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path === '/api/v1/me') return Promise.resolve(jsonResponse(200, ME))
    if (method === 'GET' && path === '/api/v1/push/vapid-public-key') return Promise.resolve(vapidKey())
    if (method === 'POST' && path === '/api/v1/push/subscriptions') {
      subscriptions.push(JSON.parse(String(init?.body)))
      return Promise.resolve(jsonResponse(200, { id: 'sub-1', endpoint: PUSH_ENDPOINT }))
    }
    if (method === 'GET' && path === '/api/v1/settings/household') {
      return Promise.resolve(jsonResponse(200, { ...HOUSEHOLD_SETTINGS, onboarding_completed_at: null }))
    }
    if (method === 'PATCH' && path === '/api/v1/settings/household') {
      return new Promise<Response>((resolve) => {
        held.push({ body: JSON.parse(String(init?.body)), answer: resolve })
      })
    }
    throw new Error(`unexpected fetch: ${method} ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, held, subscriptions }
}

function completed(request: Held) {
  request.answer(jsonResponse(200, { ...HOUSEHOLD_SETTINGS, onboarding_completed_at: COMPLETED_AT }))
}

// renderApp renders the whole app at step 3, so the first-run redirect is
// what decides where finishing lands.
async function renderApp() {
  mockViewport(390)
  const server = stubFirstRunServer()
  const api = createApiClient(
    fakeLocation('/onboarding/notifications'),
    createNetworkStatus(window),
    createVerifiedSession(localStorage),
  )
  render(
    <InDataRouter path="/onboarding/notifications">
      <App api={api} serviceWorker={undefined} />
    </InDataRouter>,
  )
  await screen.findByRole('heading', { level: 1, name: 'Stay in the loop' })
  return server
}

// renderStep renders step 3 alone, and a Home that says it was reached.
// AuthBootstrap reads /api/v1/me for the CSRF token every write needs.
async function renderStep(vapidKey?: () => Response) {
  const server = stubFirstRunServer(vapidKey)
  render(
    <InDataRouter path="/onboarding/notifications">
      <ApiProviders>
        <AuthBootstrap>
          <Routes>
            <Route path="/onboarding/notifications" element={<NotificationsStep />} />
            <Route path="/" element={<h1>Home</h1>} />
            <Route path="/onboarding/regulars" element={<h1>Regulars step</h1>} />
          </Routes>
        </AuthBootstrap>
      </ApiProviders>
    </InDataRouter>,
  )
  await screen.findByRole('heading', { level: 1, name: 'Stay in the loop' })
  return server
}

// finishFromResult taps "Finish setting up", the only action once a result
// shows, and expects onboarding finished and Home reached.
async function finishFromResult(server: ReturnType<typeof stubFirstRunServer>) {
  expect(screen.queryByRole('button', { name: 'Turn on notifications' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Maybe later' })).not.toBeInTheDocument()
  const finish = screen.getByRole('button', { name: 'Finish setting up' })
  expectMinHitArea(finish)
  // Not a silent success: onboarding is not finished behind the household's back.
  expect(server.held).toHaveLength(0)
  fireEvent.click(finish)
  await vi.waitFor(() => expect(server.held).toHaveLength(1))
  expect(server.held[0]!.body).toEqual({ onboarding_completed: true })
  completed(server.held[0]!)
  expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
}

function useIphoneSafariTab() {
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(IPHONE_SAFARI)
  Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: 5 })
  Object.defineProperty(navigator, 'standalone', { configurable: true, value: false })
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query }) as MediaQueryList)
}

function turnOn() {
  return screen.getByRole('button', { name: 'Turn on notifications' })
}

function later() {
  return screen.getByRole('button', { name: 'Maybe later' })
}

describe('onboarding step 3', () => {
  it('explains what MealQuest notifies about, with all three progress pills filled', async () => {
    await renderApp()

    expect(screen.getByText('Step 3 of 3')).toBeInTheDocument()
    const pills = Array.from(document.getElementsByClassName(styles.pill!))
    expect(pills.map((pill) => pill.classList.contains(styles.filled!))).toEqual([true, true, true])
    const topics = within(screen.getByRole('list')).getAllByRole('listitem')
    expect(topics.map((topic) => topic.textContent)).toEqual([
      expect.stringMatching(/^Plan ready/),
      expect.stringMatching(/^Supply run ready/),
      expect.stringMatching(/^Tonight's dinner/),
      expect.stringMatching(/^New Side quests/),
    ])
    expect(turnOn()).toBeEnabled()
    expect(later()).toBeEnabled()
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()
  })

  it('never asks for notification permission on render', async () => {
    await renderApp()
    expect(requestPermission).not.toHaveBeenCalled()
  })

  it('sizes both actions as tap targets and fixes them to the bottom', async () => {
    await renderApp()
    expectMinHitArea(turnOn())
    expectMinHitArea(later())
    expect(declaredStyle(turnOn()).height).toBe('52px')
    expect(declaredStyle(later()).height).toBe('52px')
    expect(declaredStyle(turnOn().parentElement!.parentElement!).position).toBe('fixed')
  })

  it('shows no install hint outside iOS Safari', async () => {
    await renderApp()
    expect(screen.queryByText('Tap Share, then Add to Home Screen to get notifications')).not.toBeInTheDocument()
  })

  it('shows the install hint in an iOS Safari tab', async () => {
    useIphoneSafariTab()
    await renderStep()
    expect(screen.getByRole('note')).toHaveTextContent('Tap Share, then Add to Home Screen to get notifications')
  })

  it('"Maybe later" finishes onboarding and goes Home, ending the first-run redirect', async () => {
    const server = await renderApp()
    fireEvent.click(later())

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]!.body).toEqual({ onboarding_completed: true })
    expect(turnOn()).toBeDisabled()
    expect(later()).toBeDisabled()
    completed(server.held[0]!)

    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    expect(requestPermission).not.toHaveBeenCalled()
  })

  it('"Turn on notifications" asks permission from the tap, subscribes this device and says it is on', async () => {
    const browser = stubPushEnvironment({ permission: 'default', answer: 'granted' })
    const server = await renderStep()
    expect(browser.notification.requestPermission).not.toHaveBeenCalled()

    fireEvent.click(turnOn())
    expect(browser.notification.requestPermission).toHaveBeenCalledOnce()
    expect(turnOn()).toBeDisabled()
    expect(later()).toBeDisabled()

    expect(await screen.findByRole('status')).toHaveTextContent('Notifications are on for this device.')
    expect(browser.pushManager.subscribe).toHaveBeenCalledOnce()
    expect(server.subscriptions).toEqual([expect.objectContaining({ endpoint: PUSH_ENDPOINT })])
    await finishFromResult(server)
    expect(browser.notification.requestPermission).toHaveBeenCalledOnce()
  })

  it('"Turn on notifications" says the browser blocked them, and how to allow them later', async () => {
    const browser = stubPushEnvironment({ permission: 'default', answer: 'denied' })
    const server = await renderStep()
    fireEvent.click(turnOn())

    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent("Notifications aren't on: this browser blocked them.")
    expect(status).toHaveTextContent("allow notifications for this site in the browser's site settings")
    expect(status).toHaveTextContent('turn them on in Settings under Notifications')
    expect(browser.pushManager.subscribe).not.toHaveBeenCalled()
    expect(server.subscriptions).toEqual([])
    await finishFromResult(server)
  })

  it('"Turn on notifications" says blocked without asking again when the browser already denied them', async () => {
    const browser = stubPushEnvironment({ permission: 'denied' })
    const server = await renderStep()
    fireEvent.click(turnOn())

    expect(await screen.findByRole('status')).toHaveTextContent("Notifications aren't on: this browser blocked them.")
    expect(browser.notification.requestPermission).not.toHaveBeenCalled()
    await finishFromResult(server)
  })

  it('"Turn on notifications" says plainly that a browser without Web Push turned nothing on', async () => {
    // The app as a whole: jsdom has no service worker or PushManager.
    const server = await renderApp()
    fireEvent.click(turnOn())

    expect(await screen.findByRole('status')).toHaveTextContent(
      "Notifications aren't on: this browser can't receive them, so nothing was turned on.",
    )
    expect(screen.getByRole('heading', { level: 1, name: 'Stay in the loop' })).toBeInTheDocument()
    await finishFromResult(server)
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    expect(requestPermission).not.toHaveBeenCalled()
  })

  it('"Turn on notifications" in an iOS Safari tab says to add MealQuest to the Home Screen first', async () => {
    useIphoneSafariTab()
    const server = await renderStep()
    fireEvent.click(turnOn())

    expect(await screen.findByRole('status')).toHaveTextContent(
      "Notifications aren't on: on iPhone and iPad they only work once MealQuest is on the Home Screen. Tap Share, then Add to Home Screen, open MealQuest from there, and turn them on in Settings under Notifications.",
    )
    expect(screen.getByRole('note')).toHaveTextContent('Tap Share, then Add to Home Screen to get notifications')
    expect(requestPermission).not.toHaveBeenCalled()
    await finishFromResult(server)
  })

  it('"Turn on notifications" can ask again after the prompt is dismissed', async () => {
    const browser = stubPushEnvironment({ permission: 'default', answer: 'default' })
    const server = await renderStep()
    fireEvent.click(turnOn())

    expect(await screen.findByRole('status')).toHaveTextContent(
      'The permission prompt was closed without an answer, so notifications are still off.',
    )
    expect(server.held).toHaveLength(0)
    await vi.waitFor(() => expect(turnOn()).toBeEnabled())
    expect(later()).toBeEnabled()

    fireEvent.click(turnOn())
    await vi.waitFor(() => expect(browser.notification.requestPermission).toHaveBeenCalledTimes(2))
  })

  it('"Maybe later" finishes without asking for permission or subscribing', async () => {
    const browser = stubPushEnvironment({ permission: 'default', answer: 'granted' })
    const server = await renderStep()
    fireEvent.click(later())

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    completed(server.held[0]!)
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
    expect(browser.notification.requestPermission).not.toHaveBeenCalled()
    expect(browser.pushManager.subscribe).not.toHaveBeenCalled()
    expect(server.subscriptions).toEqual([])
  })

  it('says why and stays when turning on fails', async () => {
    stubPushEnvironment({ permission: 'granted' })
    const server = await renderStep(() => errorResponse(503, 'push_unavailable'))
    fireEvent.click(turnOn())

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not turn on notifications: push_unavailable message')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(server.held).toHaveLength(0)
    expect(turnOn()).toBeEnabled()
    expect(screen.getByRole('heading', { level: 1, name: 'Stay in the loop' })).toBeInTheDocument()
  })

  it('refuses to ask for permission outside a tap, and says so', async () => {
    const browser = stubPushEnvironment({ permission: 'default' })
    Object.defineProperty(navigator, 'userActivation', { configurable: true, value: { isActive: false } })
    await renderStep()
    fireEvent.click(turnOn())

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not turn on notifications: subscribePush: notification permission can only be asked for from a tap on a button',
    )
    expect(browser.notification.requestPermission).not.toHaveBeenCalled()
  })

  it('says why and stays when the server refuses to finish onboarding, then finishes on retry', async () => {
    const server = await renderApp()
    fireEvent.click(later())
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(500, 'internal'))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not finish setting up: internal message')
    expect(screen.getByRole('heading', { level: 1, name: 'Stay in the loop' })).toBeInTheDocument()
    expect(later()).toBeEnabled()

    fireEvent.click(later())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    completed(server.held[1]!)
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
  })
})

describe('onboarding step 3 with too few Regulars', () => {
  it('says why and offers a way back to add Regulars', async () => {
    const server = await renderStep()
    fireEvent.click(later())
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(
      jsonResponse(400, {
        error: {
          code: 'invalid_settings',
          message: 'invalid household settings: onboarding_completed: add at least 3 Regulars first (have 2)',
          retryable: false,
        },
      }),
    )

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not finish setting up: invalid household settings: onboarding_completed: add at least 3 Regulars first (have 2)',
    )
    const addRegulars = screen.getByRole('button', { name: 'Add Regulars' })
    expectMinHitArea(addRegulars)
    fireEvent.click(addRegulars)
    expect(await screen.findByRole('heading', { level: 1, name: 'Regulars step' })).toBeInTheDocument()
  })

  it('offers no way back for any other refusal', async () => {
    const server = await renderStep()
    fireEvent.click(later())
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(500, 'internal'))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not finish setting up: internal message')
    expect(screen.queryByRole('button', { name: 'Add Regulars' })).not.toBeInTheDocument()
  })
})
