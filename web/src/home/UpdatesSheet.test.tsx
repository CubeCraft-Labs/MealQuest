import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOME } from '../test/ApiProviders.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { HOME_KEY, HOME_PATH, UPDATES_PATH, UPDATES_SEEN_PATH } from './api.ts'
import { ROTATION_REVIEWS_PATH } from './rotation.ts'
import { HomePage } from './HomePage.tsx'

afterEach(() => {
  vi.unstubAllGlobals()
})

// minutesAgo is the stored form of the instant n minutes before now.
function minutesAgo(n: number): string {
  return new Date(Date.now() - n * 60_000).toISOString()
}

// UPDATES are a GET /api/v1/updates body, newest first: the week drafted
// five minutes ago, new Side quests three hours ago and the Supply run two
// days ago.
const UPDATES = {
  updates: [
    {
      type: 'plan_ready',
      title: 'Your week is drafted',
      body: 'Take a look at the Draft board.',
      url: '/plan?week=2026-10-12',
      created_at: minutesAgo(5),
    },
    { type: 'new_suggestions', title: '5 new Side quests', body: '', url: '/discover', created_at: minutesAgo(3 * 60) },
    {
      type: 'list_ready',
      title: 'Supply run ready',
      body: 'Week 41 · Oct 5 – 11',
      url: '/plan/shopping?week=2026-10-05',
      created_at: minutesAgo(2 * 24 * 60),
    },
  ],
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

// stubServer answers "METHOD path" requests from responders, with /me; any
// other request fails the test. Home is unread until the updates are marked
// seen, as the server answers.
function stubServer(responders: Record<string, Responder> = {}) {
  let seen = false
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    [`GET ${HOME_PATH}`]: () => jsonResponse(200, { ...HOME, recentUpdates: { unread: !seen } }),
    [`GET ${UPDATES_PATH}`]: () => jsonResponse(200, UPDATES),
    [`GET ${ROTATION_REVIEWS_PATH}`]: () => jsonResponse(200, { reviews: [] }),
    [`POST ${UPDATES_SEEN_PATH}`]: () => {
      seen = true
      return new Response(null, { status: 204 })
    },
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond(init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// posts lists the bodies of the POST /updates/seen requests sent, each of
// which must carry the CSRF token.
function posts(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([input, init]) => init?.method === 'POST' && String(input) === UPDATES_SEEN_PATH)
    .map(([, init]) => {
      expect(new Headers(init?.headers).get('X-MealQuest-CSRF')).toBe(ME.csrfToken)
      return String(init?.body)
    })
}

function homeReads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => String(input) === HOME_PATH && (init?.method ?? 'GET') === 'GET')
    .length
}

function updatesReads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input) === UPDATES_PATH && (init?.method ?? 'GET') === 'GET',
  ).length
}

// Where shows the route a row went to.
function Where() {
  const location = useLocation()
  return <p data-testid="where">{location.pathname + location.search}</p>
}

// renderHome loads /me (for the CSRF token the POST needs) and renders Home,
// answering its query client.
async function renderHome() {
  const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const router = createMemoryRouter(
    [
      { path: '/', element: <HomePage /> },
      { path: '*', element: <Where /> },
    ],
    { initialEntries: ['/'] },
  )
  const queryClient = createQueryClient()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ApiProvider>,
  )
  return queryClient
}

// openSheet taps the unread bell and answers the open sheet.
async function openSheet() {
  fireEvent.click(await screen.findByRole('button', { name: 'Recent updates, unread' }))
  return screen.getByRole('dialog', { name: 'Recent updates' })
}

async function rows(sheet: HTMLElement) {
  return within(await within(sheet).findByRole('list', { name: 'Recent updates' })).getAllByRole('listitem')
}

describe('Recent updates sheet', () => {
  it('lists the updates newest first with how long ago each came', async () => {
    stubServer()
    await renderHome()
    const listed = await rows(await openSheet())

    expect(listed).toHaveLength(3)
    expect(listed[0]).toHaveTextContent('Your week is drafted')
    expect(listed[0]).toHaveTextContent('Take a look at the Draft board.')
    expect(listed[0]).toHaveTextContent('5 min ago')
    expect(listed[1]).toHaveTextContent('5 new Side quests')
    expect(listed[1]).toHaveTextContent('3 hr ago')
    expect(listed[2]).toHaveTextContent('Supply run ready')
    expect(listed[2]).toHaveTextContent('2 days ago')
    expect(within(listed[0]!).getByText('5 min ago')).toHaveAttribute('dateTime', UPDATES.updates[0]!.created_at)
  })

  it('marks the updates seen through the newest, and the grape dot clears and stays cleared', async () => {
    const fetchMock = stubServer()
    await renderHome()
    expect(await screen.findByTestId('unread-dot')).toBeInTheDocument()
    await openSheet()

    await waitFor(() => expect(posts(fetchMock)).toEqual([JSON.stringify({ through: UPDATES.updates[0]!.created_at })]))
    expect(screen.queryByTestId('unread-dot')).not.toBeInTheDocument()
    // Home is read again once the mark is saved, and the server says
    // nothing is unread: the dot does not come back.
    await waitFor(() => expect(homeReads(fetchMock)).toBe(2))
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(await screen.findByRole('button', { name: 'Recent updates' })).toBeInTheDocument()
    expect(screen.queryByTestId('unread-dot')).not.toBeInTheDocument()
  })

  it('reads the updates afresh on each opening, so one stored since the last opening is listed and marked seen', async () => {
    // The server holds the stored events, newest first, and says Home is
    // unread while the newest is after the time marked seen.
    const stored = [...UPDATES.updates]
    let seenThrough = ''
    const fetchMock = stubServer({
      [`GET ${HOME_PATH}`]: () => jsonResponse(200, { ...HOME, recentUpdates: { unread: stored[0]!.created_at > seenThrough } }),
      [`GET ${UPDATES_PATH}`]: () => jsonResponse(200, { updates: stored }),
      [`POST ${UPDATES_SEEN_PATH}`]: (init) => {
        seenThrough = (JSON.parse(String(init?.body)) as { through: string }).through
        return new Response(null, { status: 204 })
      },
    })
    const queryClient = await renderHome()
    await rows(await openSheet())
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1))
    await waitFor(() => expect(homeReads(fetchMock)).toBe(2))
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })

    // A new event is stored a moment later, well within the query client's
    // staleTime, and Home is read again, as on coming back to it.
    const reminder = {
      type: 'day_of_reminder',
      title: 'Tacos tonight',
      body: 'Dinner at 6:00.',
      url: '/plan',
      created_at: new Date().toISOString(),
    }
    stored.unshift(reminder)
    await queryClient.invalidateQueries({ queryKey: HOME_KEY })
    expect(await screen.findByTestId('unread-dot')).toBeInTheDocument()

    const listed = await rows(await openSheet())
    expect(listed).toHaveLength(4)
    expect(listed[0]).toHaveTextContent('Tacos tonight')
    await waitFor(() =>
      expect(posts(fetchMock)).toEqual([
        JSON.stringify({ through: UPDATES.updates[0]!.created_at }),
        JSON.stringify({ through: reminder.created_at }),
      ]),
    )
    await waitFor(() => expect(homeReads(fetchMock)).toBe(4))
    expect(screen.queryByTestId('unread-dot')).not.toBeInTheDocument()
  })

  it('on an immediate reopen waits for that opening’s read, and marks the newer event seen, not the list held from before', async () => {
    const stored = [...UPDATES.updates]
    let reads = 0
    let answer: (() => void) | undefined
    const fetchMock = stubServer({
      [`GET ${UPDATES_PATH}`]: () => {
        const body = { updates: [...stored] }
        reads++
        // Every reading after the first is held until the test answers it.
        if (reads === 1) return jsonResponse(200, body)
        return new Promise<Response>((resolve) => (answer = () => resolve(jsonResponse(200, body))))
      },
    })
    await renderHome()
    await rows(await openSheet())
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1))

    // Closed and opened again at once, before any cache-collection timer
    // could run, with a new event stored in between.
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    const reminder = {
      type: 'day_of_reminder',
      title: 'Tacos tonight',
      body: 'Dinner at 6:00.',
      url: '/plan',
      created_at: new Date().toISOString(),
    }
    stored.unshift(reminder)
    fireEvent.click(screen.getByRole('button', { name: /^Recent updates/ }))
    const sheet = screen.getByRole('dialog', { name: 'Recent updates' })

    // The list held from the first opening is neither shown nor marked
    // seen while this opening's read is in flight.
    await waitFor(() => expect(updatesReads(fetchMock)).toBe(2))
    expect(within(sheet).getByLabelText('Updates loading')).toBeInTheDocument()
    expect(within(sheet).queryByRole('list')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toHaveLength(1)

    answer!()
    const listed = await rows(sheet)
    expect(listed).toHaveLength(4)
    expect(listed[0]).toHaveTextContent('Tacos tonight')
    await waitFor(() =>
      expect(posts(fetchMock)).toEqual([
        JSON.stringify({ through: UPDATES.updates[0]!.created_at }),
        JSON.stringify({ through: reminder.created_at }),
      ]),
    )
  })

  it('closing the sheet while its first read is in flight cancels that read, and a reopen reads afresh', async () => {
    const stored = [...UPDATES.updates]
    let firstAborted = false
    let answerFirst: (() => void) | undefined
    let reads = 0
    const fetchMock = stubServer({
      [`GET ${UPDATES_PATH}`]: (init) => {
        const body = { updates: [...stored] }
        reads++
        if (reads > 1) return jsonResponse(200, body)
        // The first reading is held open, and fails as fetch does if it is
        // aborted.
        return new Promise<Response>((resolve, reject) => {
          answerFirst = () => resolve(jsonResponse(200, body))
          init?.signal?.addEventListener('abort', () => {
            firstAborted = true
            reject(init.signal!.reason as Error)
          })
        })
      },
    })
    await renderHome()
    await openSheet()
    await waitFor(() => expect(updatesReads(fetchMock)).toBe(1))

    // Closed before the first read answers, a new event stored, and opened
    // again at once.
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    const reminder = {
      type: 'day_of_reminder',
      title: 'Tacos tonight',
      body: 'Dinner at 6:00.',
      url: '/plan',
      created_at: new Date().toISOString(),
    }
    stored.unshift(reminder)
    fireEvent.click(screen.getByRole('button', { name: /^Recent updates/ }))
    const sheet = screen.getByRole('dialog', { name: 'Recent updates' })

    // The first read was cancelled, and the reopen sent its own.
    await waitFor(() => expect(updatesReads(fetchMock)).toBe(2))
    expect(firstAborted).toBe(true)
    // Answering the cancelled read changes nothing.
    answerFirst!()

    const listed = await rows(sheet)
    expect(listed).toHaveLength(4)
    expect(listed[0]).toHaveTextContent('Tacos tonight')
    await waitFor(() => expect(posts(fetchMock)).toEqual([JSON.stringify({ through: reminder.created_at })]))
  })

  it.each([
    [0, '/plan?week=2026-10-12'],
    [1, '/discover'],
    [2, '/plan/shopping?week=2026-10-05'],
  ])('row %i goes to %s and closes the sheet', async (i, to) => {
    stubServer()
    await renderHome()
    const listed = await rows(await openSheet())

    fireEvent.click(within(listed[i]!).getByRole('button'))
    expect(await screen.findByTestId('where')).toHaveTextContent(to)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('says there are no updates yet, and marks nothing seen', async () => {
    const fetchMock = stubServer({ [`GET ${UPDATES_PATH}`]: () => jsonResponse(200, { updates: [] }) })
    await renderHome()
    const sheet = await openSheet()

    expect(await within(sheet).findByText(/No updates yet/)).toBeInTheDocument()
    expect(within(sheet).queryByRole('list')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([])
  })

  it('puts the dot back and says so when marking the updates seen fails', async () => {
    let refuse!: (response: Response) => void
    const fetchMock = stubServer({
      [`POST ${UPDATES_SEEN_PATH}`]: () => new Promise<Response>((resolve) => (refuse = resolve)),
    })
    await renderHome()
    const sheet = await openSheet()

    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1))
    // Cleared at once, while the mark is in flight.
    expect(screen.queryByTestId('unread-dot')).not.toBeInTheDocument()

    refuse(errorResponse(500, 'internal'))
    expect(await within(sheet).findByRole('alert')).toHaveTextContent(
      "Couldn't mark your updates as seen. internal message",
    )
    expect(screen.getByTestId('unread-dot')).toBeInTheDocument()
    // The updates are still listed.
    expect(within(sheet).getByRole('list', { name: 'Recent updates' })).toBeInTheDocument()
  })

  it('reports updates it cannot load, with Retry', async () => {
    let fail = true
    stubServer({ [`GET ${UPDATES_PATH}`]: () => (fail ? errorResponse(500, 'internal') : jsonResponse(200, UPDATES)) })
    await renderHome()
    const sheet = await openSheet()

    expect(await within(sheet).findByRole('alert')).toHaveTextContent("Couldn't load your updates. internal message")
    fail = false
    fireEvent.click(within(sheet).getByRole('button', { name: 'Retry' }))
    expect(await within(sheet).findByRole('list', { name: 'Recent updates' })).toBeInTheDocument()
  })

  it('reports an update that would open another site rather than listing it', async () => {
    const fetchMock = stubServer({
      [`GET ${UPDATES_PATH}`]: () => jsonResponse(200, { updates: [{ ...UPDATES.updates[0], url: '//evil.example/plan' }] }),
    })
    await renderHome()
    const sheet = await openSheet()

    expect(await within(sheet).findByRole('alert')).toHaveTextContent(
      'update url "//evil.example/plan" is not a path on this app',
    )
    expect(within(sheet).queryByRole('list')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([])
  })
})
