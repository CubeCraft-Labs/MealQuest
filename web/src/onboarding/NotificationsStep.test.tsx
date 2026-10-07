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
import { NotificationsStep, notificationsUnavailable, type EnableNotifications } from './NotificationsStep.tsx'
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
// `held` until the test answers it.
function stubFirstRunServer() {
  const held: Held[] = []
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const path = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path === '/api/v1/me') return Promise.resolve(jsonResponse(200, ME))
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
  return { fetchMock, held }
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

// renderStep renders step 3 alone with the given enableNotifications, and a
// Home that says it was reached. AuthBootstrap reads /api/v1/me for the CSRF
// token every write needs.
async function renderStep(enableNotifications?: EnableNotifications) {
  const server = stubFirstRunServer()
  render(
    <InDataRouter path="/onboarding/notifications">
      <ApiProviders>
        <AuthBootstrap>
          <Routes>
            <Route
              path="/onboarding/notifications"
              element={<NotificationsStep enableNotifications={enableNotifications} />}
            />
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
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(IPHONE_SAFARI)
    Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: 5 })
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false })
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query }) as MediaQueryList)
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

  it('"Turn on notifications" finishes onboarding and goes Home while notifications are unavailable', async () => {
    const server = await renderApp()
    fireEvent.click(turnOn())

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]!.body).toEqual({ onboarding_completed: true })
    completed(server.held[0]!)

    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
    expect(requestPermission).not.toHaveBeenCalled()
  })

  it('"Turn on notifications" asks the injected enableNotifications first, then finishes', async () => {
    let answer: (result: 'enabled') => void = () => {}
    const enable = vi.fn<EnableNotifications>(
      () =>
        new Promise((resolve) => {
          answer = resolve
        }),
    )
    const server = await renderStep(enable)
    expect(enable).not.toHaveBeenCalled()

    fireEvent.click(turnOn())
    expect(enable).toHaveBeenCalledOnce()
    expect(turnOn()).toBeDisabled()
    expect(later()).toBeDisabled()
    // Onboarding is not finished until enableNotifications has answered.
    await Promise.resolve()
    expect(server.held).toHaveLength(0)

    answer('enabled')
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]!.body).toEqual({ onboarding_completed: true })
    completed(server.held[0]!)
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
  })

  it('"Maybe later" does not ask the injected enableNotifications', async () => {
    const enable = vi.fn<EnableNotifications>(() => Promise.resolve('enabled'))
    const server = await renderStep(enable)
    fireEvent.click(later())

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    completed(server.held[0]!)
    expect(await screen.findByRole('heading', { level: 1, name: 'Home' })).toBeInTheDocument()
    expect(enable).not.toHaveBeenCalled()
  })

  it('says why and stays when enableNotifications fails', async () => {
    const server = await renderStep(() => Promise.reject(new Error('push service down')))
    fireEvent.click(turnOn())

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not turn on notifications: push service down')
    expect(server.held).toHaveLength(0)
    expect(turnOn()).toBeEnabled()
    expect(screen.getByRole('heading', { level: 1, name: 'Stay in the loop' })).toBeInTheDocument()
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

describe('notificationsUnavailable', () => {
  it('answers "unavailable" without asking for permission', async () => {
    await expect(notificationsUnavailable()).resolves.toBe('unavailable')
    expect(requestPermission).not.toHaveBeenCalled()
  })
})
