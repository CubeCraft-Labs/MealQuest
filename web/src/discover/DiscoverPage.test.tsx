import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { stubAnimate, type FakeAnimation } from '../test/animate.ts'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { DECK_KEY } from './api.ts'
import { DiscoverPage } from './DiscoverPage.tsx'

const DECK = '/api/v1/suggestions/deck'
const QUEST_LOG = '/api/v1/dinners?status=quest_log'

// SuggestionBody is a suggestion as GET /api/v1/suggestions/deck sends it.
function suggestionBody(id: string, name: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    dinner_id: `dinner-${id}`,
    name,
    photo_url: null,
    difficulty: 'easy',
    method: 'stovetop',
    minutes_total: 25,
    cuisine: 'Korean',
    reason: 'You make Chicken stir-fry and Beef tacos most weeks. Same flavors, sweet-savory twist.',
    cited_regular_ids: ['r1', 'r2'],
    status: 'pending',
    dinner_status: 'suggested',
    decided_at: null,
    created_at: '2026-10-04T08:00:00.000Z',
    ...overrides,
  }
}

const BOWLS = suggestionBody('s1', 'Korean beef bowls', { photo_url: '/media/photos/p1-400.jpg' })
const TIKKA = suggestionBody('s2', 'Crock pot chicken tikka masala', { difficulty: 'medium', reason: 'Butter chicken night is a favorite.' })
const ORZO = suggestionBody('s3', 'Lemon garlic shrimp orzo', { difficulty: 'hard' })

type Responder = () => Response | Promise<Response>

// stubServer stands in for the API: GET of the deck answers the current
// server.deck and the Quest log the current server.questLog dinners; every
// other request is answered by responders, keyed "METHOD path". Any other
// request fails the test.
function stubServer(deck: unknown[], responders: Record<string, Responder> = {}, questLog = 6) {
  const server = { deck, questLog, fetchMock: undefined as unknown as Mock<typeof fetch> }
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    [`GET ${DECK}`]: () => jsonResponse(200, { suggestions: server.deck }),
    [`GET ${QUEST_LOG}`]: () =>
      jsonResponse(200, { dinners: Array.from({ length: server.questLog }, (_, i) => ({ id: `q${i}`, name: `Quest ${i}` })) }),
    ...responders,
  }
  server.fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond())
  })
  vi.stubGlobal('fetch', server.fetchMock)
  return server
}

// sent lists the requests other than GETs, as "METHOD path".
function sent(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([input, init]) => `${init?.method} ${String(input)}`)
}

// deckReads counts the GET /api/v1/suggestions/deck requests.
function deckReads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === DECK).length
}

// jobPolls counts the GET /api/v1/jobs/j1 requests.
function jobPolls(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === '/api/v1/jobs/j1').length
}

// Every animation finishes as it starts unless a test stubs animate again.
beforeEach(() => {
  mockViewport(390)
  stubAnimate({ autoFinish: true })
})

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(Element.prototype, 'animate')
  Reflect.deleteProperty(window, 'matchMedia')
})

// finishRunning finishes the animations running now, then lets what they
// start begin.
async function finishRunning(animations: FakeAnimation[]) {
  await act(async () => {
    animations.filter((a) => a.playState === 'running').forEach((a) => a.finish())
    await Promise.resolve()
  })
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the Discover page with queryClient.
async function renderPage(queryClient = createQueryClient()) {
  const api = createApiClient(fakeLocation('/discover'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <DiscoverPage />
      </QueryClientProvider>
    </ApiProvider>,
  )
}

function deferred() {
  let resolve!: (response: Response) => void
  const promise = new Promise<Response>((r) => (resolve = r))
  return { promise, resolve }
}

describe('DiscoverPage', () => {
  it('renders mock 1a from the deck: header, Quest log pill and three stacked cards', async () => {
    stubServer([BOWLS, TIKKA, ORZO])
    const { container } = await renderPage()

    const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
    expect(screen.getByText('Discover')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Side quests' })).toBeInTheDocument()
    const pill = await screen.findByText('Quest log · 6')
    expect(declaredStyle(pill).background).toBe('var(--color-accent-2-200)')
    expect(declaredStyle(pill).color).toBe('var(--color-accent-2-800)')

    // The deck: 440 px, perspective 1000px, three cards stacked by depth.
    const cards = container.querySelectorAll('article')
    expect(cards).toHaveLength(3)
    const deck = cards[0]!.parentElement!
    expect(getComputedStyle(deck).height).toBe('440px')
    expect(getComputedStyle(deck).perspective).toBe('1000px')
    const [top, second, third] = [...cards].map((c) => declaredStyle(c))
    expect(top!.transform).toBe('none')
    expect(top!.background).toBe('var(--color-neutral-100)')
    expect(top!.boxShadow).toBe('var(--shadow-lg)')
    expect(second!.transform).toBe('translateY(8px) scale(0.95)')
    expect(second!.background).toBe('var(--color-surface)')
    expect(third!.transform).toBe('translateY(16px) scale(0.9)')
    expect(third!.background).toBe('var(--color-neutral-300)')
    for (const style of [top, second, third]) expect(style!.borderRadius).toBe('var(--radius-card)')

    // Only the top card is read out; the ones behind it are the stack's edge.
    expect(cards[1]).toHaveAttribute('aria-hidden', 'true')
    expect(cards[2]).toHaveAttribute('aria-hidden', 'true')
    expect(screen.queryByRole('article', { name: 'Crock pot chicken tikka masala' })).not.toBeInTheDocument()

    // The top card: washed photo, Bagel name, difficulty dots, "Why this?".
    const photo = within(card).getByRole('img', { name: 'Korean beef bowls' })
    expect(photo).toHaveAttribute('src', '/media/photos/p1-400.jpg')
    expect(getComputedStyle(photo).height).toBe('220px')
    expect(photo.style.getPropertyValue('--photo-inset')).toBe('10px')
    expect(photo.style.getPropertyValue('--photo-radius')).toBe('24px')
    const name = within(card).getByRole('heading', { level: 2, name: 'Korean beef bowls' })
    expect(getComputedStyle(name).fontSize).toBe('24px')
    const difficulty = within(card).getByText('Easy')
    const dots = difficulty.querySelectorAll('[data-filled]')
    expect([...dots].map((dot) => dot.getAttribute('data-filled'))).toEqual(['true', 'false', 'false'])
    expect(getComputedStyle(dots[0]!).width).toBe('8px')
    const why = within(card).getByText('Why this?').parentElement!
    expect(why).toHaveTextContent('Why this? You make Chicken stir-fry and Beef tacos most weeks.')
    expect(declaredStyle(why).background).toBe('var(--color-accent-2-100)')

    // The stamps are there, hidden until a decision shows them.
    const tryStamp = within(card).getByText('Try it!')
    const passStamp = within(card).getByText('Pass')
    expect(getComputedStyle(tryStamp).opacity).toBe('0')
    expect(getComputedStyle(tryStamp).transform).toBe('rotate(-12deg)')
    expect(getComputedStyle(passStamp).opacity).toBe('0')
    expect(getComputedStyle(passStamp).transform).toBe('rotate(12deg)')

    // Pass and Try it: 62 px buttons with 11 px labels.
    for (const label of ['Pass', 'Try it']) {
      const button = screen.getByRole('button', { name: label })
      expect(getComputedStyle(button).fontSize).toBe('11px')
      expectMinHitArea(button)
      expect(getComputedStyle(button.querySelector('span')!).width).toBe('62px')
    }
  })

  it.each([
    [TIKKA, 'Medium', 2],
    [ORZO, 'Hard', 3],
  ] as const)('shows $1 with filled dots', async (quest, label, filled) => {
    stubServer([quest])
    await renderPage()
    const card = await screen.findByRole('article', { name: quest.name })
    expect(within(card).getByText(label).querySelectorAll('[data-filled="true"]')).toHaveLength(filled)
  })

  it('records Try it on → and Pass on ←, for the top card', async () => {
    const server = stubServer([BOWLS, TIKKA, ORZO], {
      'POST /api/v1/suggestions/s1/accept': () => {
        server.deck = [TIKKA, ORZO]
        return jsonResponse(200, { suggestion: { ...BOWLS, status: 'accepted' }, replenish_job_id: null })
      },
      'POST /api/v1/suggestions/s2/decline': () => {
        server.deck = [ORZO]
        return jsonResponse(200, { suggestion: { ...TIKKA, status: 'declined' }, replenish_job_id: null })
      },
    })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])

    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    await screen.findByRole('article', { name: 'Lemon garlic shrimp orzo' })
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept', 'POST /api/v1/suggestions/s2/decline'])
  })

  it('records Pass and Try it from the buttons', async () => {
    const server = stubServer([BOWLS, TIKKA], {
      'POST /api/v1/suggestions/s1/decline': () => {
        server.deck = [TIKKA]
        return jsonResponse(200, { suggestion: { ...BOWLS, status: 'declined' }, replenish_job_id: null })
      },
      'POST /api/v1/suggestions/s2/accept': () => {
        server.deck = []
        return jsonResponse(200, { suggestion: { ...TIKKA, status: 'accepted' }, replenish_job_id: null })
      },
    })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
    await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
    fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
    await screen.findByRole('heading', { name: 'All caught up' })
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline', 'POST /api/v1/suggestions/s2/accept'])
  })

  it('locks input while a decision is in flight', async () => {
    const accept = deferred()
    const server = stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/accept': () => accept.promise })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeDisabled())
    expect(screen.getByRole('button', { name: 'Pass' })).toBeDisabled()
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])

    server.deck = [TIKKA]
    accept.resolve(jsonResponse(200, { suggestion: { ...BOWLS, status: 'accepted' }, replenish_job_id: null }))
    await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
    expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled()
  })

  it('leaves ← / → alone with a modifier or while typing in a field', async () => {
    const server = stubServer([BOWLS])
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.keyDown(window, { key: 'ArrowRight', shiftKey: true })
    fireEvent.keyDown(window, { key: 'ArrowLeft', metaKey: true })
    const field = document.createElement('input')
    document.body.append(field)
    fireEvent.keyDown(field, { key: 'ArrowRight' })
    field.remove()
    expect(sent(server.fetchMock)).toEqual([])
  })

  it('shows a refused decision with a retry, keeping the card', async () => {
    let refuse = true
    const server = stubServer([BOWLS], {
      'POST /api/v1/suggestions/s1/accept': () => {
        if (refuse) return errorResponse(500, 'internal')
        server.deck = []
        return jsonResponse(200, { suggestion: { ...BOWLS, status: 'accepted' }, replenish_job_id: null })
      },
    })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't save that choice.")
    expect(screen.getByRole('article', { name: 'Korean beef bowls' })).toBeInTheDocument()

    refuse = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await screen.findByRole('heading', { name: 'All caught up' })
  })

  it('shows "All caught up" when the deck is empty', async () => {
    stubServer([])
    await renderPage()
    expect(await screen.findByRole('heading', { level: 2, name: 'All caught up' })).toBeInTheDocument()
    expect(screen.getByText('New side quests arrive Sunday.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Get more now' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Try it' })).not.toBeInTheDocument()
  })

  it('Get more now starts a batch and shows skeleton cards until its Side quests arrive', async () => {
    const jobs = [
      { id: 'j1', kind: 'suggestion_batch', status: 'running', result: { batch_id: 'b1', stage: 'suggesting', suggestion_ids: [], dropped: 0 }, error: null },
      { id: 'j1', kind: 'suggestion_batch', status: 'succeeded', result: { batch_id: 'b1', stage: 'done', suggestion_ids: ['s1'], dropped: 0 }, error: null },
    ]
    const server = stubServer([], {
      'POST /api/v1/suggestions/batches': () =>
        jsonResponse(202, { batch_id: 'b1', job_id: 'j1', trigger: 'manual', status: 'pending' }),
      'GET /api/v1/jobs/j1': () => {
        const job = jobs.shift()!
        if (job.status === 'succeeded') server.deck = [BOWLS]
        return jsonResponse(200, job)
      },
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Get more now' }))

    expect(await screen.findByText('Finding new side quests…')).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-card')).toHaveLength(3)
    expect(screen.getByRole('region', { name: 'Side quests loading' })).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByRole('heading', { name: 'All caught up' })).not.toBeInTheDocument()

    await screen.findByRole('article', { name: 'Korean beef bowls' }, { timeout: 3000 })
    expect(screen.queryByTestId('skeleton-card')).not.toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/batches'])
  })

  it('shows a failed batch with a retry over the empty state', async () => {
    stubServer([], {
      'POST /api/v1/suggestions/batches': () =>
        jsonResponse(202, { batch_id: 'b1', job_id: 'j1', trigger: 'manual', status: 'pending' }),
      'GET /api/v1/jobs/j1': () =>
        jsonResponse(200, { id: 'j1', kind: 'suggestion_batch', status: 'failed', result: null, error: 'model unavailable' }),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Get more now' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't find new side quests. model unavailable")
    expect(screen.getByRole('heading', { name: 'All caught up' })).toBeInTheDocument()
  })

  it('shows a deck that fails to load with a retry', async () => {
    let fail = true
    stubServer([BOWLS], { [`GET ${DECK}`]: () => (fail ? errorResponse(500, 'internal') : jsonResponse(200, { suggestions: [BOWLS] })) })
    await renderPage()
    const alert = await screen.findByRole('alert', {}, { timeout: 5000 })
    expect(alert).toHaveTextContent("Couldn't load your side quests.")
    fail = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await screen.findByRole('article', { name: 'Korean beef bowls' })
  })

  it('opens and closes the Quest log from its pill, a keyboard-reachable button', async () => {
    stubServer([BOWLS], {}, 2)
    await renderPage()
    const pill = await screen.findByRole('button', { name: 'Quest log · 2' })
    expect(pill).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('list', { name: 'Quest log' })).not.toBeInTheDocument()
    expectMinHitArea(pill)
    pill.focus()
    expect(pill).toHaveFocus()

    fireEvent.click(pill)
    expect(pill).toHaveAttribute('aria-expanded', 'true')
    const list = screen.getByRole('list', { name: 'Quest log' })
    expect(pill).toHaveAttribute('aria-controls', list.parentElement!.id)
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(['Quest 0', 'Quest 1'])

    fireEvent.click(pill)
    expect(pill).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('list', { name: 'Quest log' })).not.toBeInTheDocument()
  })

  it('says so when the Quest log it opens is empty', async () => {
    stubServer([BOWLS], {}, 0)
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Quest log · 0' }))
    expect(screen.getByText('No dinners in the Quest log yet. Try it on a Side quest to add one.')).toBeInTheDocument()
  })

  it('sends one decision for presses made before the page re-renders', async () => {
    const accept = deferred()
    const server = stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/accept': () => accept.promise })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
    await waitFor(() => expect(sent(server.fetchMock)).toContain('POST /api/v1/suggestions/s1/accept'))
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
    expect(screen.getByRole('button', { name: 'Try it' })).toBeDisabled()

    server.deck = [TIKKA]
    accept.resolve(jsonResponse(200, { suggestion: { ...BOWLS, status: 'accepted' }, replenish_job_id: null }))
    await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    await waitFor(() =>
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept', 'POST /api/v1/suggestions/s2/decline']),
    )
  })

  it.each([
    ['not a decision', {}],
    ['another suggestion', { suggestion: { ...TIKKA, status: 'accepted' }, replenish_job_id: null }],
    ['not accepted', { suggestion: { ...BOWLS, status: 'pending' }, replenish_job_id: null }],
  ])('shows a decision answered with %s as unsaved, keeping the card', async (_, answer) => {
    stubServer([BOWLS], { 'POST /api/v1/suggestions/s1/accept': () => jsonResponse(200, answer) })
    await renderPage()
    await screen.findByRole('article', { name: 'Korean beef bowls' })

    fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't save that choice. POST /api/v1/suggestions/s1/accept: response is not the accepted suggestion")
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.getByRole('article', { name: 'Korean beef bowls' })).toBeInTheDocument()
  })

  it('shows a batch whose job cannot be polled with a retry, keeping the skeleton cards', async () => {
    let failPoll = true
    const server = stubServer([], {
      'POST /api/v1/suggestions/batches': () =>
        jsonResponse(202, { batch_id: 'b1', job_id: 'j1', trigger: 'manual', status: 'pending' }),
      'GET /api/v1/jobs/j1': () => {
        if (failPoll) return errorResponse(500, 'internal')
        server.deck = [BOWLS]
        return jsonResponse(200, {
          id: 'j1',
          kind: 'suggestion_batch',
          status: 'succeeded',
          result: { batch_id: 'b1', stage: 'done', suggestion_ids: ['s1'], dropped: 0 },
          error: null,
        })
      },
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Get more now' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't check on new side quests.")
    expect(screen.getAllByTestId('skeleton-card')).toHaveLength(3)

    // Retry polls the job at once, well before the next poll is due; it
    // does not ask for another batch.
    failPoll = false
    const polls = jobPolls(server.fetchMock)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(jobPolls(server.fetchMock)).toBe(polls + 1), { timeout: 200 })
    await screen.findByRole('article', { name: 'Korean beef bowls' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/batches'])
  })

  it('shows a batch whose job answers with an unknown status as an error, and Retry polls the job again', async () => {
    let status = 'stuck'
    const server = stubServer([], {
      'POST /api/v1/suggestions/batches': () =>
        jsonResponse(202, { batch_id: 'b1', job_id: 'j1', trigger: 'manual', status: 'pending' }),
      'GET /api/v1/jobs/j1': () => {
        if (status === 'succeeded') server.deck = [BOWLS]
        return jsonResponse(200, {
          id: 'j1',
          kind: 'suggestion_batch',
          status,
          result: { batch_id: 'b1', stage: 'done', suggestion_ids: ['s1'], dropped: 0 },
          error: null,
        })
      },
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Get more now' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      "Couldn't check on new side quests. GET /api/v1/jobs/j1: response is not the suggestion_batch job j1",
    )
    // Polling has stopped: nothing more is asked until Retry.
    await new Promise((resolve) => setTimeout(resolve, 700))
    expect(jobPolls(server.fetchMock)).toBe(1)

    status = 'succeeded'
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await screen.findByRole('article', { name: 'Korean beef bowls' })
    expect(jobPolls(server.fetchMock)).toBe(2)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/batches'])
  })

  it('shows a batch whose job result is unreadable with a retry', async () => {
    stubServer([], {
      'POST /api/v1/suggestions/batches': () =>
        jsonResponse(202, { batch_id: 'b1', job_id: 'j1', trigger: 'manual', status: 'pending' }),
      'GET /api/v1/jobs/j1': () =>
        jsonResponse(200, { id: 'j1', kind: 'suggestion_batch', status: 'succeeded', result: { batch_id: 'b1' }, error: null }),
    })
    await renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Get more now' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't find new side quests. GET /api/v1/jobs/j1: result is not a suggestion batch")
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'All caught up' })).toBeInTheDocument()
  })

  describe('optimistic decisions', () => {
    const PAELLA = suggestionBody('s4', 'Sheet pan paella')
    const SOUP = suggestionBody('s5', 'Tortellini soup')
    const accepted = (quest: typeof BOWLS, replenish: string | null = null) =>
      jsonResponse(200, { suggestion: { ...quest, status: 'accepted' }, replenish_job_id: replenish })
    const declined = (quest: typeof BOWLS) => jsonResponse(200, { suggestion: { ...quest, status: 'declined' }, replenish_job_id: null })
    // topCard is the deck's top card, the one read out.
    const topCard = () => screen.getAllByRole('article')[0]

    it('Try it takes the card off the deck and bumps the Quest log count once the exit finishes, before the answer', async () => {
      const accept = deferred()
      const server = stubServer([BOWLS, TIKKA, ORZO, PAELLA], { 'POST /api/v1/suggestions/s1/accept': () => accept.promise })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      await screen.findByText('Quest log · 6')

      // Nothing is applied on the press: the card is still leaving.
      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      expect(screen.getByRole('article', { name: 'Korean beef bowls' })).toBeInTheDocument()
      expect(screen.getByText('Quest log · 6')).toBeInTheDocument()
      expect(sent(server.fetchMock)).toEqual([])

      // Once it is gone the decision is applied, and sent.
      await finishRunning(animations)
      await finishRunning(animations)
      await screen.findByText('Quest log · 7')
      expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala')
      expect(screen.queryByRole('article', { name: 'Korean beef bowls' })).not.toBeInTheDocument()
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
      expect(screen.getByRole('button', { name: 'Try it' })).toBeDisabled()

      server.deck = [TIKKA, ORZO, PAELLA]
      server.questLog = 7
      accept.resolve(accepted(BOWLS))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled())
      expect(screen.getByText('Quest log · 7')).toBeInTheDocument()
      expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala')
      // Three Side quests are left: the deck is not read again.
      expect(deckReads(server.fetchMock)).toBe(1)
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    })

    it('Pass takes the card off the deck before the answer, leaving the Quest log count', async () => {
      const decline = deferred()
      const server = stubServer([BOWLS, TIKKA, ORZO, PAELLA], { 'POST /api/v1/suggestions/s1/decline': () => decline.promise })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      await screen.findByText('Quest log · 6')

      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      await waitFor(() => expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala'))
      expect(screen.queryByRole('article', { name: 'Korean beef bowls' })).not.toBeInTheDocument()
      expect(screen.getByText('Quest log · 6')).toBeInTheDocument()
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline'])

      server.deck = [TIKKA, ORZO, PAELLA]
      decline.resolve(declined(BOWLS))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Pass' })).toBeEnabled())
      expect(screen.getByText('Quest log · 6')).toBeInTheDocument()
      expect(deckReads(server.fetchMock)).toBe(1)
    })

    it('puts a refused Try it back on top of the deck, reverts the Quest log count and offers Retry', async () => {
      const accept = deferred()
      let refuse = true
      const server = stubServer([BOWLS, TIKKA, ORZO, PAELLA], {
        'POST /api/v1/suggestions/s1/accept': () => (refuse ? accept.promise : accepted(BOWLS)),
      })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      await screen.findByText('Quest log · 6')

      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      await screen.findByText('Quest log · 7')
      expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala')

      accept.resolve(errorResponse(500, 'internal'))
      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't save that choice.")
      expect(topCard()).toHaveAccessibleName('Korean beef bowls')
      expect(screen.getAllByRole('article', { hidden: true }).map((card) => card.querySelector('h2')!.textContent)).toEqual([
        'Korean beef bowls',
        'Crock pot chicken tikka masala',
        'Lemon garlic shrimp orzo',
      ])
      expect(screen.getByText('Quest log · 6')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled()

      // Retry sends the same decision again, applied again at once.
      refuse = false
      server.deck = [TIKKA, ORZO, PAELLA]
      server.questLog = 7
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept', 'POST /api/v1/suggestions/s1/accept'])
      expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala')
      expect(screen.getByText('Quest log · 7')).toBeInTheDocument()
    })

    it('keeps a saved decision off the deck when a read started during it answers late', async () => {
      const decline = deferred()
      let late: ReturnType<typeof deferred> | null = null
      const server = stubServer([BOWLS, TIKKA, ORZO, PAELLA], {
        'POST /api/v1/suggestions/s1/decline': () => decline.promise,
        [`GET ${DECK}`]: () => late?.promise ?? jsonResponse(200, { suggestions: server.deck }),
      })
      const queryClient = createQueryClient()
      await renderPage(queryClient)
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      await waitFor(() => expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline']))
      expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala')

      // A read of the deck starts before the decision is saved, and answers
      // only after: with the Side quest still pending.
      late = deferred()
      const stale = late
      await act(async () => void queryClient.invalidateQueries({ queryKey: DECK_KEY }))
      expect(deckReads(server.fetchMock)).toBe(2)
      late = null
      server.deck = [TIKKA, ORZO, PAELLA]
      decline.resolve(declined(BOWLS))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Pass' })).toBeEnabled())
      await act(async () => stale.resolve(jsonResponse(200, { suggestions: [BOWLS, TIKKA, ORZO, PAELLA] })))

      // The late answer is dropped, and the deck is read again after the save.
      await act(async () => await new Promise((resolve) => setTimeout(resolve, 0)))
      expect(screen.queryByRole('article', { name: 'Korean beef bowls' })).not.toBeInTheDocument()
      await waitFor(() => expect(deckReads(server.fetchMock)).toBe(3))
      expect(screen.getAllByRole('article', { hidden: true }).map((card) => card.querySelector('h2')!.textContent)).toEqual([
        'Crock pot chicken tikka masala',
        'Lemon garlic shrimp orzo',
        'Sheet pan paella',
      ])
    })

    it('leaves the Quest log count alone for a dinner already in it, on Try it and on its refusal', async () => {
      const accept = deferred()
      stubServer([BOWLS, TIKKA, ORZO, PAELLA], {
        'POST /api/v1/suggestions/s1/accept': () => accept.promise,
        // A read of the Quest log already has the dinner the stale deck offers.
        [`GET ${QUEST_LOG}`]: () =>
          jsonResponse(200, {
            dinners: [
              { id: 'q0', name: 'Quest 0' },
              { id: 'dinner-s1', name: 'Korean beef bowls' },
            ],
          }),
      })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      await screen.findByText('Quest log · 2')

      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      await waitFor(() => expect(topCard()).toHaveAccessibleName('Crock pot chicken tikka masala'))
      expect(screen.getByText('Quest log · 2')).toBeInTheDocument()

      accept.resolve(errorResponse(500, 'internal'))
      await screen.findByRole('alert')
      expect(topCard()).toHaveAccessibleName('Korean beef bowls')
      expect(screen.getByText('Quest log · 2')).toBeInTheDocument()
    })

    it('shows a decision refused because the deck changed during the exit, with Retry', async () => {
      const server = stubServer([BOWLS])
      const queryClient = createQueryClient()
      const animations = stubAnimate({ autoFinish: false })
      await renderPage(queryClient)
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      // While the card leaves, a read of the deck takes the last card away.
      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      server.deck = []
      await act(async () => await queryClient.invalidateQueries({ queryKey: DECK_KEY }))
      await screen.findByRole('heading', { name: 'All caught up' })

      await finishRunning(animations)
      await finishRunning(animations)
      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't save that choice. suggestion s1 is not in the deck")
      expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
      expect(sent(server.fetchMock)).toEqual([])
    })

    it('reads the deck again once a saved decision leaves fewer than 3 cards', async () => {
      const server = stubServer([BOWLS, TIKKA, ORZO], {
        'POST /api/v1/suggestions/s1/decline': () => {
          server.deck = [TIKKA, ORZO, PAELLA, SOUP]
          return declined(BOWLS)
        },
      })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      expect(deckReads(server.fetchMock)).toBe(1)

      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      await waitFor(() => expect(deckReads(server.fetchMock)).toBe(2))
      await waitFor(() =>
        expect(screen.getAllByRole('article', { hidden: true }).map((card) => card.querySelector('h2')!.textContent)).toEqual([
          'Crock pot chicken tikka masala',
          'Lemon garlic shrimp orzo',
          'Sheet pan paella',
        ]),
      )
    })

    it('shows skeleton cards, not "All caught up", while the replenish job a decision started runs', async () => {
      const polls = [
        { id: 'j1', kind: 'suggestion_batch', status: 'running', result: { batch_id: 'b1', stage: 'suggesting', suggestion_ids: [], dropped: 0 }, error: null },
        { id: 'j1', kind: 'suggestion_batch', status: 'succeeded', result: { batch_id: 'b1', stage: 'done', suggestion_ids: ['s2'], dropped: 0 }, error: null },
      ]
      const server = stubServer([BOWLS], {
        'POST /api/v1/suggestions/s1/accept': () => {
          server.deck = []
          return accepted(BOWLS, 'j1')
        },
        'GET /api/v1/jobs/j1': () => {
          const job = polls.shift()!
          if (job.status === 'succeeded') server.deck = [TIKKA]
          return jsonResponse(200, job)
        },
      })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      expect(await screen.findByText('Finding new side quests…')).toBeInTheDocument()
      expect(screen.getAllByTestId('skeleton-card')).toHaveLength(3)
      expect(screen.getByRole('region', { name: 'Side quests loading' })).toHaveAttribute('aria-busy', 'true')
      expect(screen.queryByRole('heading', { name: 'All caught up' })).not.toBeInTheDocument()

      await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' }, { timeout: 3000 })
      expect(screen.queryByTestId('skeleton-card')).not.toBeInTheDocument()
      expect(screen.queryByRole('heading', { name: 'All caught up' })).not.toBeInTheDocument()
      // The decision's replenish is followed; no batch is asked for.
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
    })

    it('shows skeleton cards, not "All caught up", while the decision that emptied the deck is unanswered', async () => {
      const accept = deferred()
      const server = stubServer([BOWLS], { 'POST /api/v1/suggestions/s1/accept': () => accept.promise })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      await waitFor(() => expect(screen.getAllByTestId('skeleton-card')).toHaveLength(3))
      expect(screen.queryByRole('heading', { name: 'All caught up' })).not.toBeInTheDocument()
      expect(screen.queryByText('Finding new side quests…')).not.toBeInTheDocument()

      // Saved without a replenish, the deck is caught up.
      server.deck = []
      accept.resolve(accepted(BOWLS))
      await screen.findByRole('heading', { name: 'All caught up' })
      expect(screen.queryByTestId('skeleton-card')).not.toBeInTheDocument()
    })
  })

  describe('motion', () => {
    const accepted = (quest: typeof BOWLS) => jsonResponse(200, { suggestion: { ...quest, status: 'accepted' }, replenish_job_id: null })
    const declined = (quest: typeof BOWLS) => jsonResponse(200, { suggestion: { ...quest, status: 'declined' }, replenish_job_id: null })
    const on = (animations: FakeAnimation[], target: Element) => animations.filter((a) => a.target === target)

    it('Try it bumps the button, pops the stamp, wiggles and throws the card, then records it, bumps the Quest log pill and promotes the deck', async () => {
      const server = stubServer([BOWLS, TIKKA, ORZO], {
        'POST /api/v1/suggestions/s1/accept': () => {
          server.deck = [TIKKA, ORZO]
          return accepted(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const pill = await screen.findByText('Quest log · 6')
      const tryButton = screen.getByRole('button', { name: 'Try it' })

      fireEvent.click(tryButton)
      expect(animations.map((a) => a.target)).toEqual([tryButton, within(card).getByText('Try it!'), card])
      const [bumped, pop, wiggle] = animations
      expect(bumped!.keyframes.map((k) => k.transform)).toEqual(['scale(1)', 'scale(.88)', 'scale(1.06)', 'scale(1)'])
      expect(bumped!.options).toEqual({ duration: 300, easing: 'ease-out' })
      expect(pop!.options).toEqual({ duration: 220, easing: 'cubic-bezier(.34,1.4,.64,1)', fill: 'forwards' })
      expect(wiggle!.keyframes.map((k) => k.transform)).toEqual(['rotate(0deg)', 'rotate(-4deg)', 'rotate(4deg)', 'rotate(-3deg)', 'rotate(2deg)', 'rotate(0deg)'])
      expect(wiggle!.options).toEqual({ duration: 420, easing: 'ease-in-out' })

      // Input is locked while the card leaves, and nothing is sent yet.
      await waitFor(() => expect(tryButton).toBeDisabled())
      expect(screen.getByRole('button', { name: 'Pass' })).toBeDisabled()
      fireEvent.keyDown(window, { key: 'ArrowLeft' })
      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      await finishRunning(animations)
      const fly = animations[3]!
      expect(fly.target).toBe(card)
      expect(fly.keyframes).toEqual([
        { transform: 'translate(0px,0px) rotate(0deg)' },
        { transform: 'translate(-14px,4px) rotate(-3deg)', offset: 0.18 },
        { transform: 'translate(150%,-60px) rotate(28deg)', opacity: 0 },
      ])
      expect(fly.options).toEqual({ duration: 480, easing: 'cubic-bezier(.55,0,.8,.4)', fill: 'forwards' })
      expect(animations).toHaveLength(4)
      expect(sent(server.fetchMock)).toEqual([])

      // Once it is gone: the decision, the pill bump, then the deck moves up.
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
      expect(on(animations, pill).map((a) => a.options)).toEqual([{ duration: 300, easing: 'ease-out' }])
      expect(on(animations, pill)[0]!.keyframes.map((k) => k.transform)).toEqual(['scale(1)', 'scale(.88)', 'scale(1.06)', 'scale(1)'])
      const next = await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      const behind = next.parentElement!.querySelectorAll('article')[1]!
      expect(on(animations, next).map((a) => [a.keyframes, a.options])).toEqual([
        [[{ transform: 'translateY(8px) scale(0.95)' }, { transform: 'translateY(0px) scale(1)' }], { duration: 300, easing: 'cubic-bezier(.2,.8,.2,1)' }],
      ])
      expect(on(animations, behind).map((a) => a.keyframes)).toEqual([
        [{ transform: 'translateY(16px) scale(0.9)' }, { transform: 'translateY(8px) scale(0.95)' }],
      ])
      await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled())
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
    })

    it('Pass bumps the button and folds the card under its shade, then records it and promotes the deck', async () => {
      const server = stubServer([BOWLS, TIKKA], {
        'POST /api/v1/suggestions/s1/decline': () => {
          server.deck = [TIKKA]
          return declined(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      const { container } = await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const pill = await screen.findByText('Quest log · 6')
      const passButton = screen.getByRole('button', { name: 'Pass' })
      const shade = card.querySelector('[data-part="shade"]')!
      expect(declaredStyle(shade).background).toBe('var(--color-text)')
      expect(getComputedStyle(shade).opacity).toBe('0')

      fireEvent.keyDown(window, { key: 'ArrowLeft' })
      expect(animations.map((a) => a.target)).toEqual([passButton, shade, card])
      const [bumped, darken, fold] = animations
      expect(bumped!.options).toEqual({ duration: 300, easing: 'ease-out' })
      expect(darken!.keyframes).toEqual([{ opacity: 0 }, { opacity: 0.4, offset: 0.45 }, { opacity: 0.55 }])
      expect(darken!.options).toEqual({ duration: 720, fill: 'forwards' })
      expect(fold!.keyframes).toEqual([
        { transformOrigin: '0% 50%', transform: 'translate(0px,0px) rotate(0deg) rotateY(0deg)', easing: 'cubic-bezier(.45,0,.85,.55)' },
        { transformOrigin: '0% 50%', transform: 'translate(0px,0px) rotate(0deg) rotateY(-80deg)', offset: 0.45, easing: 'cubic-bezier(.4,0,.9,.6)' },
        { transformOrigin: '0% 50%', transform: 'translate(-45%,560px) rotate(-35deg) rotateY(-90deg)', opacity: 0 },
      ])
      expect(fold!.options).toEqual({ duration: 720, fill: 'forwards' })
      await waitFor(() => expect(passButton).toBeDisabled())
      expect(sent(server.fetchMock)).toEqual([])

      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline'])
      const next = await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      expect(on(animations, next).map((a) => a.options)).toEqual([{ duration: 300, easing: 'cubic-bezier(.2,.8,.2,1)' }])
      expect(on(animations, pill)).toEqual([])
      expect(container.querySelectorAll('article')).toHaveLength(1)
      await waitFor(() => expect(passButton).toBeEnabled())
    })

    it.each([
      ['Try it', 'ArrowRight', 'accept'],
      ['Pass', 'ArrowLeft', 'decline'],
    ] as const)('%s with reduced motion is a 200 ms fade, with no bump, wiggle, fold or promotion', async (_, key, action) => {
      mockViewport(390, { reducedMotion: true })
      const server = stubServer([BOWLS, TIKKA, ORZO], {
        [`POST /api/v1/suggestions/s1/${action}`]: () => {
          server.deck = [TIKKA, ORZO]
          return action === 'accept' ? accepted(BOWLS) : declined(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      await screen.findByText('Quest log · 6')

      fireEvent.keyDown(window, { key })
      expect(animations).toHaveLength(1)
      expect(animations[0]!.target).toBe(card)
      expect(animations[0]!.keyframes).toEqual([{ opacity: 1 }, { opacity: 0 }])
      expect(animations[0]!.options).toEqual({ duration: 200, fill: 'forwards' })
      await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeDisabled())
      expect(sent(server.fetchMock)).toEqual([])

      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual([`POST /api/v1/suggestions/s1/${action}`])
      await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled())
      expect(animations).toHaveLength(1)
    })

    it('puts the card back when its decision is refused, and unlocks', async () => {
      stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/accept': () => errorResponse(500, 'internal') })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      await finishRunning(animations)
      await finishRunning(animations)
      await screen.findByRole('alert')
      // The stamp, wiggle and throw are undone; the bump had ended.
      expect(animations.slice(1, 4).map((a) => a.playState)).toEqual(['idle', 'idle', 'idle'])
      expect(screen.getByRole('article', { name: 'Korean beef bowls' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled()
    })

    it('stops the exit and sends nothing when the page goes away mid-exit', async () => {
      const server = stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      const { unmount } = await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      unmount()
      await act(async () => {
        await Promise.resolve()
      })
      expect(animations.slice(1).map((a) => a.playState)).toEqual(['idle', 'idle'])
      expect(sent(server.fetchMock)).toEqual([])
    })
  })

  describe('swipe', () => {
    const accepted = (quest: typeof BOWLS) => jsonResponse(200, { suggestion: { ...quest, status: 'accepted' }, replenish_job_id: null })
    const declined = (quest: typeof BOWLS) => jsonResponse(200, { suggestion: { ...quest, status: 'declined' }, replenish_job_id: null })
    const on = (animations: FakeAnimation[], target: Element) => animations.filter((a) => a.target === target)
    const SPRING = 'cubic-bezier(.34,1.4,.64,1)'
    let capture: Mock<(pointerId: number) => void>

    // jsdom has no pointer capture.
    beforeEach(() => {
      capture = vi.fn()
      Element.prototype.setPointerCapture = capture
    })

    afterEach(() => {
      Reflect.deleteProperty(Element.prototype, 'setPointerCapture')
    })

    // press, drag and release put a pointer down on the card at (200, 300),
    // move it by dx, dy, and let go where it is, or dx, dy from the press.
    let pointer = { dx: 0, dy: 0 }
    function press(card: Element) {
      pointer = { dx: 0, dy: 0 }
      fireEvent.pointerDown(card, { pointerId: 7, isPrimary: true, button: 0, clientX: 200, clientY: 300 })
    }
    function drag(card: Element, dx: number, dy: number) {
      pointer = { dx, dy }
      fireEvent.pointerMove(card, { pointerId: 7, isPrimary: true, clientX: 200 + dx, clientY: 300 + dy })
    }
    function release(card: Element, dx = pointer.dx, dy = pointer.dy) {
      fireEvent.pointerUp(card, { pointerId: 7, isPrimary: true, button: 0, clientX: 200 + dx, clientY: 300 + dy })
    }
    const stamps = (card: HTMLElement) => [within(card).getByText('Try it!'), within(card).getByText('Pass')]

    it('makes the top card follow the pointer, captured, with the stamps showing how far it has gone', async () => {
      stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp, passStamp] = stamps(card)
      expect(declaredStyle(card).getPropertyValue('touch-action')).toBe('none')

      press(card)
      expect(capture).toHaveBeenCalledExactlyOnceWith(7)
      expect(capture.mock.contexts[0]).toBe(card)

      drag(card, 54, 20)
      expect(card.style.transform).toBe('translate(54px,6px) rotate(3deg)')
      expect(tryStamp!.style.opacity).toBe('0.54')
      expect(passStamp!.style.opacity).toBe('0')

      drag(card, -36, -10)
      expect(card.style.transform).toBe('translate(-36px,-3px) rotate(-2deg)')
      expect(tryStamp!.style.opacity).toBe('0')
      expect(passStamp!.style.opacity).toBe('0.36')

      drag(card, -180, 0)
      expect(tryStamp!.style.opacity).toBe('0')
      expect(passStamp!.style.opacity).toBe('1')
      expect(animations).toEqual([])
    })

    it('Try it on a release past 110 px right: throws the card from where it was let go, in 340 ms with no wiggle, then records it', async () => {
      const server = stubServer([BOWLS, TIKKA, ORZO], {
        'POST /api/v1/suggestions/s1/accept': () => {
          server.deck = [TIKKA, ORZO]
          return accepted(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const pill = await screen.findByText('Quest log · 6')
      const tryButton = screen.getByRole('button', { name: 'Try it' })

      press(card)
      drag(card, 126, 30)
      release(card)
      // No button bump, no wiggle: the stamp pops and the card flies.
      expect(animations.map((a) => a.target)).toEqual([within(card).getByText('Try it!')])
      await waitFor(() => expect(tryButton).toBeDisabled())
      expect(animations).toHaveLength(2)
      const fly = animations[1]!
      expect(fly.target).toBe(card)
      expect(fly.keyframes).toEqual([
        { transform: 'translate(126px,9px) rotate(7deg)' },
        { transform: 'translate(150%,-60px) rotate(28deg)', opacity: 0 },
      ])
      expect(fly.options).toEqual({ duration: 340, easing: 'cubic-bezier(.55,0,.8,.4)', fill: 'forwards' })
      expect(sent(server.fetchMock)).toEqual([])

      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
      expect(on(animations, pill)).toHaveLength(1)
      const next = await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      expect(on(animations, next).map((a) => a.options)).toEqual([{ duration: 300, easing: 'cubic-bezier(.2,.8,.2,1)' }])
      await waitFor(() => expect(tryButton).toBeEnabled())
      expect(next.style.transform).toBe('')
    })

    it('Pass on a release past 110 px left: folds the card from where it was let go, then records it', async () => {
      const server = stubServer([BOWLS, TIKKA], {
        'POST /api/v1/suggestions/s1/decline': () => {
          server.deck = [TIKKA]
          return declined(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const pill = await screen.findByText('Quest log · 6')
      const shade = card.querySelector('[data-part="shade"]')!

      press(card)
      drag(card, -144, 20)
      release(card)
      expect(animations.map((a) => a.target)).toEqual([shade, card])
      expect(animations[1]!.keyframes).toEqual([
        { transformOrigin: '0% 50%', transform: 'translate(-144px,6px) rotate(-8deg) rotateY(0deg)', easing: 'cubic-bezier(.45,0,.85,.55)' },
        { transformOrigin: '0% 50%', transform: 'translate(-144px,6px) rotate(-8deg) rotateY(-80deg)', offset: 0.45, easing: 'cubic-bezier(.4,0,.9,.6)' },
        { transformOrigin: '0% 50%', transform: 'translate(-45%,560px) rotate(-35deg) rotateY(-90deg)', opacity: 0 },
      ])
      expect(animations[1]!.options).toEqual({ duration: 720, fill: 'forwards' })
      await waitFor(() => expect(screen.getByRole('button', { name: 'Pass' })).toBeDisabled())
      expect(sent(server.fetchMock)).toEqual([])

      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline'])
      await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      expect(on(animations, pill)).toEqual([])
    })

    it.each([
      ['right', 110],
      ['left', -110],
    ])('springs the card back from a release of 110 px %s, resetting the stamps and deciding nothing', async (_, dx) => {
      const server = stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp, passStamp] = stamps(card)

      press(card)
      drag(card, dx, 0)
      expect([tryStamp!.style.opacity, passStamp!.style.opacity]).toContain('1')
      release(card)
      expect(animations).toHaveLength(1)
      expect(animations[0]!.target).toBe(card)
      expect(animations[0]!.keyframes).toEqual([{ transform: `translate(${dx}px,0px) rotate(${dx / 18}deg)` }, { transform: 'translate(0px,0px) rotate(0deg)' }])
      expect(animations[0]!.options).toEqual({ duration: 420, easing: SPRING })
      expect(card.style.transform).toBe('')
      expect(tryStamp!.style.opacity).toBe('')
      expect(passStamp!.style.opacity).toBe('')
      expect(getComputedStyle(tryStamp!).opacity).toBe('0')
      expect(getComputedStyle(passStamp!).opacity).toBe('0')

      // A later move with the pointer up does nothing.
      drag(card, 200, 0)
      expect(card.style.transform).toBe('')
      await act(async () => {
        await Promise.resolve()
      })
      expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled()
      expect(sent(server.fetchMock)).toEqual([])
    })

    it.each([
      ['Try it', 60, 130, 'accept'],
      ['Pass', -60, -130, 'decline'],
    ] as const)('%s from where a release past 110 px lets go, though the last move was short of it', async (_, moved, at, action) => {
      const server = stubServer([BOWLS, TIKKA], {
        [`POST /api/v1/suggestions/s1/${action}`]: () => {
          server.deck = [TIKKA]
          return action === 'accept' ? accepted(BOWLS) : declined(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, moved, 0)
      release(card, at, 20)
      const from = `translate(${at}px,6px) rotate(${at / 18}deg)`
      await waitFor(() => expect(on(animations, card)).toHaveLength(1))
      expect(on(animations, card).map((a) => a.keyframes[0]!.transform)).toEqual([action === 'accept' ? from : `${from} rotateY(0deg)`])
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual([`POST /api/v1/suggestions/s1/${action}`])
    })

    it.each([
      ['right', 150, 40],
      ['left', -150, -40],
    ])('springs the card back from where a release short of 110 px %s lets go, though the last move was past it', async (_, moved, at) => {
      const server = stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, moved, 0)
      release(card, at, 0)
      expect(animations.map((a) => [a.target, a.keyframes, a.options])).toEqual([
        [card, [{ transform: `translate(${at}px,0px) rotate(${at / 18}deg)` }, { transform: 'translate(0px,0px) rotate(0deg)' }], { duration: 420, easing: SPRING }],
      ])
      expect(card.style.transform).toBe('')
      await act(async () => {
        await Promise.resolve()
      })
      expect(sent(server.fetchMock)).toEqual([])
    })

    it('stops a snap-back when the card is grabbed again, so it follows the new drag', async () => {
      stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, 80, 0)
      release(card)
      const [spring] = animations
      expect(spring!.playState).toBe('running')

      press(card)
      expect(spring!.playState).toBe('idle')
      drag(card, 36, 0)
      expect(card.style.transform).toBe('translate(36px,0px) rotate(2deg)')
      expect(animations).toHaveLength(1)
    })

    it('stops a snap-back when a decision starts', async () => {
      stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/decline': () => declined(BOWLS) })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, -80, 0)
      release(card)
      const [spring] = animations
      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      expect(spring!.playState).toBe('idle')
      expect(on(animations, card)).toHaveLength(2)
    })

    it('springs the card back when it loses the pointer capture before the release, deciding nothing', async () => {
      const server = stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/accept': () => accepted(BOWLS) })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp, passStamp] = stamps(card)

      press(card)
      drag(card, 150, 0)
      fireEvent.lostPointerCapture(card, { pointerId: 7, isPrimary: true })
      expect(animations.map((a) => [a.target, a.options])).toEqual([[card, { duration: 420, easing: SPRING }]])
      expect(card.style.transform).toBe('')
      expect(tryStamp!.style.opacity).toBe('')
      expect(passStamp!.style.opacity).toBe('')

      // The drag is over: later moves and the release do nothing.
      drag(card, 180, 0)
      release(card)
      expect(card.style.transform).toBe('')
      expect(animations).toHaveLength(1)
      await act(async () => {
        await Promise.resolve()
      })
      expect(sent(server.fetchMock)).toEqual([])

      // The capture going after a release leaves its decision alone.
      press(card)
      drag(card, 140, 0)
      release(card)
      await waitFor(() => expect(on(animations, card)).toHaveLength(2))
      fireEvent.lostPointerCapture(card, { pointerId: 7, isPrimary: true })
      expect(animations).toHaveLength(3)
      expect(card.style.transform).toBe('translate(140px,0px) rotate(7.777777777777778deg)')
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
    })

    it('springs the card back when the pointer is canceled, however far it went', async () => {
      const server = stubServer([BOWLS])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, 180, 0)
      fireEvent.pointerCancel(card, { pointerId: 7, isPrimary: true })
      expect(animations.map((a) => a.options)).toEqual([{ duration: 420, easing: SPRING }])
      expect(card.style.transform).toBe('')
      expect(sent(server.fetchMock)).toEqual([])
    })

    it('ignores a drag while busy', async () => {
      const server = stubServer([BOWLS, TIKKA], { 'POST /api/v1/suggestions/s1/decline': () => declined(BOWLS) })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.click(screen.getByRole('button', { name: 'Pass' }))
      const started = animations.length
      press(card)
      drag(card, 160, 0)
      release(card)
      expect(capture).not.toHaveBeenCalled()
      expect(card.style.transform).toBe('')
      expect(animations).toHaveLength(started)
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/decline'])
    })

    it('puts a drag held while a Retry goes out back at once on release, deciding nothing', async () => {
      const retry = deferred()
      let calls = 0
      const server = stubServer([BOWLS, TIKKA], {
        'POST /api/v1/suggestions/s1/accept': () => (++calls === 1 ? errorResponse(500, 'internal') : retry.promise),
      })
      await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      fireEvent.click(screen.getByRole('button', { name: 'Try it' }))
      const alert = await screen.findByRole('alert')
      // The refused Side quest is back on top of the deck.
      const card = screen.getByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, 60, 0)
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      expect(screen.getByRole('button', { name: 'Try it' })).toBeDisabled()
      drag(card, -160, 0)
      expect(card.style.transform).toBe('translate(60px,0px) rotate(3.3333333333333335deg)')
      const started = vi.mocked(Element.prototype.animate).mock.calls.length
      release(card)
      expect(card.style.transform).toBe('')
      expect(within(card).getByText('Try it!').style.opacity).toBe('')
      expect(vi.mocked(Element.prototype.animate).mock.calls.length).toBe(started)
      await waitFor(() =>
        expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept', 'POST /api/v1/suggestions/s1/accept']),
      )
      // The retried Side quest leaves the deck while the Retry is out.
      await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      retry.resolve(accepted(BOWLS))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled())
    })

    it('drags only the top card', async () => {
      const server = stubServer([BOWLS, TIKKA, ORZO])
      const animations = stubAnimate({ autoFinish: false })
      const { container } = await renderPage()
      await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [, second, third] = container.querySelectorAll<HTMLElement>('article')

      for (const card of [second!, third!]) {
        expect(declaredStyle(card).getPropertyValue('touch-action')).toBe('')
        press(card)
        drag(card, 160, 0)
        release(card)
        expect(card.style.transform).toBe('')
      }
      expect(capture).not.toHaveBeenCalled()
      expect(animations).toEqual([])
      expect(sent(server.fetchMock)).toEqual([])
    })

    it('ignores a press that is not the primary button', async () => {
      stubServer([BOWLS])
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      fireEvent.pointerDown(card, { pointerId: 7, isPrimary: true, button: 2, clientX: 200, clientY: 300 })
      drag(card, 160, 0)
      expect(capture).not.toHaveBeenCalled()
      expect(card.style.transform).toBe('')
    })

    it.each([
      ['without moving', null],
      ['after moving', 160],
    ])('ignores a second primary pointer pressing and releasing %s mid-drag, then springs the card back clean', async (_, moves) => {
      const server = stubServer([BOWLS, TIKKA])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp, passStamp] = stamps(card)
      // A touch (id 7) drags; a mouse (id 1), also primary, comes and goes.
      const mouse = (x: number) => ({ pointerId: 1, pointerType: 'mouse', isPrimary: true, button: 0, clientX: 400 + x, clientY: 300 })

      press(card)
      drag(card, 72, 0)
      fireEvent.pointerDown(card, mouse(0))
      if (moves !== null) fireEvent.pointerMove(card, mouse(moves))
      fireEvent.pointerUp(card, mouse(moves ?? 0))
      fireEvent.lostPointerCapture(card, mouse(moves ?? 0))
      expect(capture).toHaveBeenCalledExactlyOnceWith(7)
      expect(card.style.transform).toBe('translate(72px,0px) rotate(4deg)')
      expect(animations).toEqual([])

      release(card)
      expect(animations.map((a) => [a.target, a.options])).toEqual([[card, { duration: 420, easing: SPRING }]])
      expect(card.style.transform).toBe('')
      expect(tryStamp!.style.opacity).toBe('')
      expect(passStamp!.style.opacity).toBe('')
      await act(async () => {
        await Promise.resolve()
      })
      expect(screen.getByRole('button', { name: 'Try it' })).toBeEnabled()
      expect(sent(server.fetchMock)).toEqual([])

      // The card can be dragged again.
      press(card)
      drag(card, 36, 0)
      expect(card.style.transform).toBe('translate(36px,0px) rotate(2deg)')
    })

    it('a key decision mid-drag ends the drag and throws the card from rest', async () => {
      const server = stubServer([BOWLS, TIKKA], {
        'POST /api/v1/suggestions/s1/accept': () => {
          server.deck = [TIKKA]
          return accepted(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp] = stamps(card)

      press(card)
      drag(card, -60, 0)
      fireEvent.keyDown(window, { key: 'ArrowRight' })
      expect(card.style.transform).toBe('')
      expect(tryStamp!.style.opacity).toBe('')
      // The button bump, stamp pop and wiggle of a Try it from rest.
      expect(animations.map((a) => a.target)).toEqual([screen.getByRole('button', { name: 'Try it' }), tryStamp, card])
      release(card)
      expect(animations).toHaveLength(3)
      await finishRunning(animations)
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/s1/accept'])
    })

    it('puts a swiped card back at rest when its decision is refused', async () => {
      stubServer([BOWLS], { 'POST /api/v1/suggestions/s1/decline': () => errorResponse(500, 'internal') })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })
      const [tryStamp, passStamp] = stamps(card)

      press(card)
      drag(card, -150, 0)
      release(card)
      await finishRunning(animations)
      await screen.findByRole('alert')
      expect(animations.map((a) => a.playState)).toEqual(['idle', 'idle'])
      expect(card.style.transform).toBe('')
      expect(tryStamp!.style.opacity).toBe('')
      expect(passStamp!.style.opacity).toBe('')
      expect(screen.getByRole('button', { name: 'Pass' })).toBeEnabled()
    })

    it.each([
      ['Try it', 140, 'accept'],
      ['Pass', -140, 'decline'],
    ] as const)('%s by swipe with reduced motion fades the card where it was let go', async (_, dx, action) => {
      mockViewport(390, { reducedMotion: true })
      const server = stubServer([BOWLS, TIKKA], {
        [`POST /api/v1/suggestions/s1/${action}`]: () => {
          server.deck = [TIKKA]
          return action === 'accept' ? accepted(BOWLS) : declined(BOWLS)
        },
      })
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, dx, 0)
      release(card)
      expect(animations.map((a) => [a.target, a.keyframes, a.options])).toEqual([[card, [{ opacity: 1 }, { opacity: 0 }], { duration: 200, fill: 'forwards' }]])
      expect(card.style.transform).toBe(`translate(${dx}px,0px) rotate(${dx / 18}deg)`)
      await finishRunning(animations)
      expect(sent(server.fetchMock)).toEqual([`POST /api/v1/suggestions/s1/${action}`])
      await screen.findByRole('article', { name: 'Crock pot chicken tikka masala' })
      expect(animations).toHaveLength(1)
    })

    it('puts the card back at once, without the spring, from a short swipe with reduced motion', async () => {
      mockViewport(390, { reducedMotion: true })
      stubServer([BOWLS])
      const animations = stubAnimate({ autoFinish: false })
      await renderPage()
      const card = await screen.findByRole('article', { name: 'Korean beef bowls' })

      press(card)
      drag(card, 80, 0)
      release(card)
      expect(animations).toEqual([])
      expect(card.style.transform).toBe('')
      expect(within(card).getByText('Try it!').style.opacity).toBe('')
    })
  })
})
