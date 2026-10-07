import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
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
// server.deck and the Quest log has questLog dinners; every other request
// is answered by responders, keyed "METHOD path". Any other request fails
// the test.
function stubServer(deck: unknown[], responders: Record<string, Responder> = {}, questLog = 6) {
  const server = { deck, fetchMock: undefined as unknown as Mock<typeof fetch> }
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    [`GET ${DECK}`]: () => jsonResponse(200, { suggestions: server.deck }),
    [`GET ${QUEST_LOG}`]: () =>
      jsonResponse(200, { dinners: Array.from({ length: questLog }, (_, i) => ({ id: `q${i}`, name: `Quest ${i}` })) }),
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

afterEach(() => {
  vi.unstubAllGlobals()
})

// renderPage loads /me (for the CSRF token every write needs) and renders
// the Discover page.
async function renderPage() {
  const api = createApiClient(fakeLocation('/discover'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
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

    failPoll = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await screen.findByRole('article', { name: 'Korean beef bowls' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual(['POST /api/v1/suggestions/batches', 'POST /api/v1/suggestions/batches'])
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
})
