import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { clickPath, openNotification, parsePushPayload, type ClickWindow } from '../push/notification.ts'
import { QueryClientProvider } from '@tanstack/react-query'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession, ME_PATH } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOME } from '../test/ApiProviders.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { HOME_PATH } from './api.ts'
import { HomePage } from './HomePage.tsx'
import { rotationReviewPath, ROTATION_REVIEWS_PATH } from './rotation.ts'

// SHAKSHUKA and RAMEN are pending Rotation checks, oldest first, as GET
// /api/v1/rotation-reviews sends them.
const SHAKSHUKA = { dinner: { id: 'd7', name: 'Shakshuka', photo: '/media/photos/p7-400.jpg' }, plan_date: '2026-10-01' }
const RAMEN = { dinner: { id: 'd8', name: 'Miso ramen', photo: null }, plan_date: '2026-10-02' }

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(navigator, 'serviceWorker')
})

type Deferred = { resolve: (response: Response) => void; path: string; body: unknown }

// stubApi answers Home's reads like the server: the pending Rotation checks
// are reviews, and each POST /api/v1/dinners/{id}/rotation-review waits in
// decisions until the test answers it; a 200 answer takes the check off the
// server's list, as the server does.
function stubApi(reviews: (typeof SHAKSHUKA | typeof RAMEN)[]) {
  const server = [...reviews]
  const decisions: Deferred[] = []
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const path = String(input)
    if (path === ME_PATH) return Promise.resolve(jsonResponse(200, ME))
    if (path === HOME_PATH) return Promise.resolve(jsonResponse(200, HOME))
    if (path === ROTATION_REVIEWS_PATH) return Promise.resolve(jsonResponse(200, { reviews: server }))
    const review = server.find((r) => rotationReviewPath(r.dinner.id) === path)
    if (init?.method === 'POST' && review !== undefined) {
      expect(new Headers(init.headers).get('X-MealQuest-CSRF')).toBe(ME.csrfToken)
      const body: unknown = JSON.parse(String(init.body))
      return new Promise<Response>((resolve) =>
        decisions.push({
          path,
          body,
          resolve: (response) => {
            if (response.ok) server.splice(server.indexOf(review), 1)
            resolve(response)
          },
        }),
      )
    }
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, decisions, server }
}

// decided is the server's answer to a decision on review.
function decided(review: typeof SHAKSHUKA | typeof RAMEN, decision: string, dinnerStatus: string) {
  return jsonResponse(200, {
    dinner: review.dinner,
    plan_date: review.plan_date,
    decision,
    dinner_status: dinnerStatus,
    decided_at: '2026-10-02T13:00:00Z',
    updated_by: 'sub-1',
  })
}

// renderHome loads /me (for the CSRF token an answer needs) and renders Home
// at path.
async function renderHome(path = '/') {
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const router = createMemoryRouter([{ path: '/', element: <HomePage /> }], { initialEntries: [path] })
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ApiProvider>,
  )
}

const card = () => screen.findByRole('region', { name: 'Keep it in rotation?' })
const posts = (fetchMock: ReturnType<typeof stubApi>['fetchMock']) =>
  fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')

describe('RotationCheck', () => {
  it('shows no card when nothing waits for an answer', async () => {
    const { fetchMock } = stubApi([])
    await renderHome()

    expect(await screen.findByRole('heading', { name: 'Pizza night' })).toBeInTheDocument()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(ROTATION_REVIEWS_PATH, expect.anything()))
    expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Quick dinner' })).toBeInTheDocument()
  })

  it('asks about the oldest pending check above the tiles, and decides nothing by itself', async () => {
    const { fetchMock } = stubApi([SHAKSHUKA, RAMEN])
    await renderHome()

    const check = await card()
    expect(within(check).getByText('Last night: Shakshuka')).toBeInTheDocument()
    expect(within(check).getByRole('heading', { level: 2, name: 'Keep it in rotation?' })).toBeInTheDocument()
    expect(within(check).getByRole('img', { name: 'Shakshuka' })).toHaveAttribute('src', '/media/photos/p7-400.jpg')
    expect(screen.queryByText('Last night: Miso ramen')).not.toBeInTheDocument()

    const keep = within(check).getByRole('button', { name: 'Keep it' })
    const drop = within(check).getByRole('button', { name: 'Not for us' })
    const notMade = within(check).getByRole('button', { name: "We didn't make it" })
    expect(keep.className).toMatch(/primary/)
    expect(drop.className).toMatch(/outline/)
    expect(notMade.className).toMatch(/notMade/)
    for (const button of [keep, drop, notMade]) expectMinHitArea(button)

    const tiles = screen.getByRole('link', { name: 'Quick dinner' })
    expect(check.compareDocumentPosition(tiles) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getByRole('region', { name: 'Tonight' }).compareDocumentPosition(check) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    // The card waits for the household: nothing is sent unasked.
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)))
    expect(posts(fetchMock)).toHaveLength(0)
    expect(await card()).toBeInTheDocument()
  })

  it.each([
    ['Keep it', 'keep', 'regular', 'Added to your Regulars'],
    ['Not for us', 'drop', 'archived', "Got it — we won't suggest it again"],
    ["We didn't make it", 'not_made', 'quest_log', 'Back in your Quest log'],
  ])('"%s" sends %s at once and confirms "%s"', async (label, decision, status, confirmation) => {
    const { fetchMock, decisions } = stubApi([SHAKSHUKA])
    await renderHome()

    fireEvent.click(within(await card()).getByRole('button', { name: label }))

    // Applied before the server answers.
    expect(await screen.findByRole('status')).toHaveTextContent(confirmation)
    expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument()
    await waitFor(() => expect(decisions).toHaveLength(1))
    expect(decisions[0]).toMatchObject({ path: '/api/v1/dinners/d7/rotation-review', body: { decision } })
    expect(posts(fetchMock)).toHaveLength(1)

    act(() => decisions[0]!.resolve(decided(SHAKSHUKA, decision, status)))
    // Rotation checks are read again once saved; none is left.
    await waitFor(() =>
      expect(fetchMock.mock.calls.filter(([path]) => path === ROTATION_REVIEWS_PATH).length).toBeGreaterThanOrEqual(2),
    )
    expect(screen.getByRole('status')).toHaveTextContent(confirmation)
    expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('reveals the next pending check at once, its answers locked until the first is saved', async () => {
    const { decisions } = stubApi([SHAKSHUKA, RAMEN])
    await renderHome()

    fireEvent.click(within(await card()).getByRole('button', { name: 'Keep it' }))

    expect(await screen.findByText('Last night: Miso ramen')).toBeInTheDocument()
    const next = await card()
    expect(within(next).getByRole('img', { name: 'Miso ramen' })).toHaveTextContent('dish photo')
    expect(screen.getByRole('status')).toHaveTextContent('Added to your Regulars')
    for (const button of within(next).getAllByRole('button')) expect(button).toBeDisabled()

    await waitFor(() => expect(decisions).toHaveLength(1))
    act(() => decisions[0]!.resolve(decided(SHAKSHUKA, 'keep', 'regular')))
    await waitFor(() => expect(within(next).getByRole('button', { name: 'Not for us' })).toBeEnabled())

    fireEvent.click(within(next).getByRole('button', { name: 'Not for us' }))
    expect(await screen.findByRole('status')).toHaveTextContent("Got it — we won't suggest it again")
    await waitFor(() => expect(decisions).toHaveLength(2))
    expect(decisions[1]).toMatchObject({ path: '/api/v1/dinners/d8/rotation-review', body: { decision: 'drop' } })
    act(() => decisions[1]!.resolve(decided(RAMEN, 'drop', 'archived')))
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument())
    expect(screen.queryByText(/Last night:/)).not.toBeInTheDocument()
  })

  it('puts the check back with an ErrorBanner when the answer fails', async () => {
    const { decisions } = stubApi([SHAKSHUKA, RAMEN])
    await renderHome()

    fireEvent.click(within(await card()).getByRole('button', { name: 'Keep it' }))
    expect(await screen.findByText('Last night: Miso ramen')).toBeInTheDocument()
    await waitFor(() => expect(decisions).toHaveLength(1))

    act(() => decisions[0]!.resolve(errorResponse(500, 'internal')))

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't save that answer. internal message")
    const check = await card()
    expect(within(check).getByText('Last night: Shakshuka')).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(within(check).getByRole('button', { name: 'Keep it' })).toBeEnabled()
  })

  it('reports an answer that is not that check decided that way, and shows what the server has', async () => {
    const { decisions } = stubApi([SHAKSHUKA])
    await renderHome()

    fireEvent.click(within(await card()).getByRole('button', { name: 'Keep it' }))
    await waitFor(() => expect(decisions).toHaveLength(1))
    act(() => decisions[0]!.resolve(decided(SHAKSHUKA, 'drop', 'archived')))

    expect(await screen.findByRole('alert')).toHaveTextContent('response is not the Rotation check decided keep')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    // The server closed the check, so the list read again has none.
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument())
  })

  it('reports Rotation checks it cannot load, with Retry', async () => {
    const answers = [errorResponse(500, 'internal'), jsonResponse(200, { reviews: [SHAKSHUKA] })]
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input) =>
        Promise.resolve(
          String(input) === ME_PATH
            ? jsonResponse(200, ME)
            : String(input) === HOME_PATH
              ? jsonResponse(200, HOME)
              : answers.shift()!,
        ),
      ),
    )
    await renderHome()

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the Rotation check. internal message")
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(within(await card()).getByText('Last night: Shakshuka')).toBeInTheDocument()
  })

  it('keeps a card already shown, with an ErrorBanner beside it, when reading the checks again fails', async () => {
    // navigator.serviceWorker is where the worker's postMessage to this
    // window arrives; a notification tap there reads the checks again.
    const container = new EventTarget()
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: container })
    const answers = [jsonResponse(200, { reviews: [SHAKSHUKA] }), errorResponse(500, 'internal')]
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>((input) =>
        Promise.resolve(
          String(input) === ME_PATH
            ? jsonResponse(200, ME)
            : String(input) === HOME_PATH
              ? jsonResponse(200, HOME)
              : answers.shift()!,
        ),
      ),
    )
    await renderHome()
    expect(within(await card()).getByText('Last night: Shakshuka')).toBeInTheDocument()

    const origin = 'https://mealquest.test'
    const win = {
      url: `${origin}/`,
      focused: true,
      focus: vi.fn(async () => {}),
      navigate: vi.fn(async () => {}),
      postMessage: (message) => container.dispatchEvent(new MessageEvent('message', { data: message })),
    } satisfies ClickWindow
    await act(() =>
      openNotification({ url: '/' }, { matchAll: async () => [win], openWindow: vi.fn(async () => null) }, origin),
    )

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the Rotation check. internal message")
    const shown = screen.getByRole('region', { name: 'Keep it in rotation?' })
    expect(within(shown).getByText('Last night: Shakshuka')).toBeInTheDocument()
    expect(within(shown).getByRole('button', { name: 'Keep it' })).toBeEnabled()
  })

  it('opens Home with the card from a rotation_review push notification', async () => {
    stubApi([SHAKSHUKA])
    const origin = 'https://mealquest.test'
    const { notice } = parsePushPayload(
      JSON.stringify({ event: 'rotation_review', title: 'Keep it in rotation?', body: 'How was Shakshuka?', url: '/' }),
      origin,
    )
    const path = clickPath({ url: notice.url }, origin)
    expect(path).toBe('/')

    await renderHome(path)
    expect(within(await card()).getByText('Last night: Shakshuka')).toBeInTheDocument()
  })

  it('shows the card when a rotation_review notification focuses Home already open, without a remount', async () => {
    // navigator.serviceWorker is where the worker's postMessage to this
    // window arrives.
    const container = new EventTarget()
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: container })
    const { fetchMock, server } = stubApi([])
    const reads = () => fetchMock.mock.calls.filter(([path]) => path === ROTATION_REVIEWS_PATH).length
    await renderHome('/')
    const home = await screen.findByRole('heading', { name: 'Pizza night' })
    await waitFor(() => expect(reads()).toBe(1))
    expect(screen.queryByRole('region', { name: 'Keep it in rotation?' })).not.toBeInTheDocument()

    // Within the cached list's staleTime the Rotation check comes due and
    // its notification is tapped while Home is open at /.
    server.push(SHAKSHUKA)
    const origin = 'https://mealquest.test'
    const win = {
      url: `${origin}/`,
      focused: true,
      focus: vi.fn(async () => {}),
      navigate: vi.fn(async () => {}),
      postMessage: (message) => container.dispatchEvent(new MessageEvent('message', { data: message })),
    } satisfies ClickWindow
    await act(() =>
      openNotification({ url: '/' }, { matchAll: async () => [win], openWindow: vi.fn(async () => null) }, origin),
    )

    expect(win.navigate).not.toHaveBeenCalled()
    expect(within(await card()).getByText('Last night: Shakshuka')).toBeInTheDocument()
    expect(reads()).toBe(2)
    expect(screen.getByRole('heading', { name: 'Pizza night' })).toBe(home)
  })
})
