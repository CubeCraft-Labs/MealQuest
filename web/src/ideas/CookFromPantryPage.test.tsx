import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { dinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { CookFromPantryPage } from './CookFromPantryPage.tsx'
import styles from './CookFromPantryPage.module.css'

const FIND = 'POST /api/v1/ideas/from-ingredients'

const INGREDIENTS = ['Chicken thighs', 'Rice', 'Spinach', 'Coconut milk', 'Garlic']

// idea is a grounded idea as the job's result carries it.
function idea(name: string, summary: string, minutes: number | null = 30) {
  return {
    name,
    uses: INGREDIENTS,
    missing: ['1 lime'],
    summary,
    method: 'stovetop',
    minutes_total: minutes,
    difficulty: 'easy',
    quick: minutes !== null && minutes <= 30,
    recipe_id: `r-${name}`,
    photo_url: '',
    source_url: 'https://recipes.example/x',
    site_name: 'Recipes',
  }
}

// The ideas of mock 1i.
const CURRY = idea('Coconut chicken curry', 'Uses all 5 · missing: 1 lime')
// BOWLS's source is withheld, as the server sends one whose link could
// carry a secret: the card shows all the same.
const BOWLS = { ...idea('Garlic spinach rice bowls', 'Uses 4 of 5 · nothing missing', 25), source_url: null, site_name: null }
const ONE_PAN = idea('One-pan chicken & rice', 'Uses 3 of 5 · missing: onion')

type JobBody = { status: string; result: unknown; error: string | null }

// running is the ideas_from_ingredients job's state at stage with ideas.
function state(status: string, stage: string, ideas: unknown[], quickOnly = false, error: string | null = null): JobBody {
  return { status, result: { stage, ingredients: INGREDIENTS, quick_only: quickOnly, ideas, dropped: 0 }, error }
}

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']

// tonightPlan is the week's plan as cook-tonight answers it, the idea's
// dinner on Thursday.
function tonightPlan() {
  return {
    id: 'plan-1',
    week_start: '2026-10-05',
    status: 'draft',
    ai_note: null,
    calendar_synced_at: null,
    days: DAYS.map((date, i) => ({
      date,
      dinner: { id: `d${i}`, name: i === 3 ? CURRY.name : `Dinner ${i}`, status: i === 3 ? 'quest_log' : 'regular', photo: null, minutes: 30, method: 'stovetop', origin: 'regular', meal_source: 'cook' },
      is_new: i === 3,
      start_by: null,
      meal_source: 'cook',
    })),
    updated_at: '2026-10-08T15:00:00Z',
  }
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

// stubServer answers "METHOD path" requests from responders, with /me; any
// other request fails the test. jobs holds each job's state, which every
// poll of it answers, so a test moves a job on by changing it.
function stubServer(responders: Record<string, Responder>) {
  const jobs: Record<string, JobBody> = {}
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    if (key === 'GET /api/v1/me') return Promise.resolve(jsonResponse(200, ME))
    const job = /^GET \/api\/v1\/jobs\/([^/]+)$/.exec(key)
    if (job !== null && !Object.hasOwn(responders, key)) {
      const id = job[1]!
      const body = jobs[id]
      if (body === undefined) throw new Error(`unexpected fetch: ${key}`)
      return Promise.resolve(jsonResponse(200, { id, kind: 'ideas_from_ingredients', ...body }))
    }
    const respond = Object.hasOwn(responders, key) ? responders[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond(init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, jobs }
}

// calls are the requests sent as "METHOD path", in order.
function calls(fetchMock: ReturnType<typeof stubServer>['fetchMock'], key: string) {
  return fetchMock.mock.calls.filter(([input, init]) => `${init?.method ?? 'GET'} ${String(input)}` === key)
}

function bodyOf(call: Parameters<typeof fetch>) {
  return JSON.parse(String(call[1]?.body)) as unknown
}

async function renderPage() {
  const api = createApiClient(fakeLocation('/cook-from-pantry'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter initialEntries={['/cook-from-pantry']}>
          <Routes>
            <Route path="/cook-from-pantry" element={<CookFromPantryPage />} />
            <Route path="/plan" element={<p>Plan screen</p>} />
            <Route path="/" element={<p>Home screen</p>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

function input() {
  return screen.getByRole('textbox', { name: 'Add ingredient' })
}

function chips() {
  return within(screen.getByRole('list', { name: 'Ingredients on hand' }))
    .queryAllByRole('listitem')
    .map((item) => item.textContent)
}

function findButton() {
  return screen.getByRole('button', { name: 'Find ideas' })
}

// enter types each ingredient and presses Enter.
function enter(...ingredients: string[]) {
  for (const ingredient of ingredients) {
    fireEvent.change(input(), { target: { value: ingredient } })
    fireEvent.keyDown(input(), { key: 'Enter' })
  }
}

function card(name: string) {
  return screen.getByRole('article', { name })
}

const SLOW = { timeout: 3000 }

// searched lists the mock's ingredients, presses Find ideas and waits for
// the job's first poll.
async function searched(fetchMock: ReturnType<typeof stubServer>['fetchMock'], jobId = 'j1') {
  enter(...INGREDIENTS)
  fireEvent.click(findButton())
  await waitFor(() => expect(calls(fetchMock, `GET /api/v1/jobs/${jobId}`).length).toBeGreaterThan(0))
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('CookFromPantryPage', () => {
  it('renders mock 1i: back button, title, chip field, filter and a disabled Find ideas', async () => {
    stubServer({})
    await renderPage()

    expect(screen.getByRole('heading', { level: 1, name: 'Cook from what I have' })).toBeInTheDocument()
    expect(input()).toHaveAttribute('placeholder', 'Add ingredient…')
    expect(input().closest(`.${styles.field}`)).not.toBeNull()
    const filter = screen.getByRole('radiogroup', { name: 'Which dinners' })
    expect(within(filter).getByRole('radio', { name: 'Any time' })).toBeChecked()
    expect(within(filter).getByRole('radio', { name: 'Quick only' })).not.toBeChecked()
    expect(findButton()).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Back to Home' }))
    expect(screen.getByText('Home screen')).toBeInTheDocument()
  })

  describe('chip entry', () => {
    it('adds an ingredient on Enter or a comma, and Find ideas needs one', async () => {
      stubServer({})
      await renderPage()

      enter('Chicken thighs')
      expect(chips()).toEqual(['Chicken thighs'])
      expect(input()).toHaveValue('')
      expect(findButton()).toBeEnabled()

      fireEvent.change(input(), { target: { value: 'Rice,' } })
      expect(chips()).toEqual(['Chicken thighs', 'Rice'])
      expect(input()).toHaveValue('')

      // Pasted text is split at its commas; what follows the last is still
      // being typed.
      fireEvent.change(input(), { target: { value: ' Spinach ,  coconut   milk, Gar' } })
      expect(chips()).toEqual(['Chicken thighs', 'Rice', 'Spinach', 'coconut milk'])
      expect(input()).toHaveValue(' Gar')

      // Enter on nothing adds nothing.
      fireEvent.change(input(), { target: { value: '   ' } })
      fireEvent.keyDown(input(), { key: 'Enter' })
      expect(chips()).toHaveLength(4)
    })

    it('enables Find ideas for text typed and not yet added, and searches it', async () => {
      const { fetchMock } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: 'j1' }) })
      await renderPage()

      fireEvent.change(input(), { target: { value: '   ' } })
      expect(findButton()).toBeDisabled()
      fireEvent.change(input(), { target: { value: 'Chicken thighs' } })
      expect(chips()).toEqual([])
      expect(findButton()).toBeEnabled()

      fireEvent.click(findButton())
      await waitFor(() => expect(calls(fetchMock, FIND)).toHaveLength(1))
      expect(bodyOf(calls(fetchMock, FIND)[0]!)).toEqual({ ingredients: ['Chicken thighs'], quick_only: false })
      expect(chips()).toEqual(['Chicken thighs'])
    })

    it('removes a chip with its cross, or the last one with Backspace in the empty input', async () => {
      stubServer({})
      await renderPage()
      enter('Chicken thighs', 'Rice', 'Spinach')

      fireEvent.click(screen.getByRole('button', { name: 'Remove Rice' }))
      expect(chips()).toEqual(['Chicken thighs', 'Spinach'])
      expect(input()).toHaveFocus()

      fireEvent.keyDown(input(), { key: 'Backspace' })
      expect(chips()).toEqual(['Chicken thighs'])

      fireEvent.click(screen.getByRole('button', { name: 'Remove Chicken thighs' }))
      expect(chips()).toEqual([])
      expect(findButton()).toBeDisabled()
    })

    it('says why it leaves out a repeated, overlong or 21st ingredient', async () => {
      stubServer({})
      await renderPage()
      enter('Rice', ' rice ')
      expect(chips()).toEqual(['Rice'])
      expect(screen.getByRole('status')).toHaveTextContent('rice is listed already.')

      enter('x'.repeat(81))
      expect(chips()).toEqual(['Rice'])
      expect(screen.getByRole('status')).toHaveTextContent('An ingredient can be at most 80 characters.')

      enter(...Array.from({ length: 19 }, (_, i) => `Item ${i}`))
      expect(chips()).toHaveLength(20)
      expect(screen.queryByRole('status')).toBeNull()
      enter('One too many')
      expect(chips()).toHaveLength(20)
      expect(screen.getByRole('status')).toHaveTextContent('List at most 20 ingredients.')
    })
  })

  it('fills the results progressively as the server grounds ideas, then enables the actions', async () => {
    const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: 'j1' }) })
    jobs.j1 = state('running', 'suggesting', [])
    await renderPage()

    enter(...INGREDIENTS)
    fireEvent.change(input(), { target: { value: 'lime' } })
    fireEvent.click(findButton())
    // What is still typed is listed before the search.
    expect(chips()).toEqual([...INGREDIENTS, 'lime'])
    await waitFor(() => expect(calls(fetchMock, FIND)).toHaveLength(1))
    expect(bodyOf(calls(fetchMock, FIND)[0]!)).toEqual({ ingredients: [...INGREDIENTS, 'lime'], quick_only: false })

    // Suggesting: five skeleton cards, no idea yet.
    expect(await screen.findByText('Thinking up ideas…')).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-card')).toHaveLength(5)
    expect(screen.queryAllByRole('article')).toHaveLength(0)

    // Grounding: the idea grounded so far, and skeletons for the rest; its
    // actions wait for the job to end.
    jobs.j1 = state('running', 'grounding', [CURRY])
    await screen.findByRole('article', { name: CURRY.name }, SLOW)
    expect(screen.getByText('Finding recipes for them…')).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-card')).toHaveLength(4)
    expect(within(card(CURRY.name)).getByRole('button', { name: 'Cook tonight' })).toBeDisabled()
    expect(within(card(CURRY.name)).getByRole('button', { name: 'Add to Quest log' })).toBeDisabled()

    jobs.j1 = state('running', 'grounding', [CURRY, BOWLS])
    await screen.findByRole('article', { name: BOWLS.name }, SLOW)
    expect(screen.getAllByTestId('skeleton-card')).toHaveLength(3)

    jobs.j1 = state('succeeded', 'done', [CURRY, BOWLS, ONE_PAN])
    await screen.findByText('3 ideas', {}, SLOW)
    expect(screen.queryAllByTestId('skeleton-card')).toHaveLength(0)
    expect(screen.getAllByRole('article').map((a) => a.getAttribute('aria-label'))).toEqual([CURRY.name, BOWLS.name, ONE_PAN.name])
    const curry = within(card(CURRY.name))
    // The server's summary line, as it is.
    expect(curry.getByText('Uses all 5 · missing: 1 lime')).toHaveClass(styles.summary!)
    expect(card(CURRY.name).querySelector(`.${styles.time}`)).toHaveTextContent('30 min')
    expect(card(BOWLS.name).querySelector(`.${styles.time}`)).toHaveTextContent('25 min')
    expect(curry.getByRole('button', { name: 'Cook tonight' })).toBeEnabled()
    expect(curry.getByRole('button', { name: 'Add to Quest log' })).toBeEnabled()
  })

  it('leaves the time off an idea whose recipe does not say it', async () => {
    const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: 'j1' }) })
    jobs.j1 = state('succeeded', 'done', [idea('Fried rice', 'Uses 2 of 5 · nothing missing', null)])
    await renderPage()
    await searched(fetchMock)
    const fried = await screen.findByRole('article', { name: 'Fried rice' })
    expect(within(fried).queryByText(/min/)).toBeNull()
  })

  describe('Any time | Quick only', () => {
    it('sends quick_only, and asks again when changed after a search', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: `j${++n}` }) })
      jobs.j1 = state('succeeded', 'done', [CURRY, BOWLS], true)
      jobs.j2 = state('succeeded', 'done', [ONE_PAN], false)
      await renderPage()

      // Before a search it only sets what Find ideas asks for.
      fireEvent.click(screen.getByRole('radio', { name: 'Quick only' }))
      expect(calls(fetchMock, FIND)).toHaveLength(0)

      await searched(fetchMock, 'j1')
      expect(bodyOf(calls(fetchMock, FIND)[0]!)).toEqual({ ingredients: INGREDIENTS, quick_only: true })
      await screen.findByText('2 ideas')

      fireEvent.click(screen.getByRole('radio', { name: 'Any time' }))
      await waitFor(() => expect(calls(fetchMock, FIND)).toHaveLength(2))
      expect(bodyOf(calls(fetchMock, FIND)[1]!)).toEqual({ ingredients: INGREDIENTS, quick_only: false })
      await screen.findByText('1 idea')
      expect(screen.getAllByRole('article').map((a) => a.getAttribute('aria-label'))).toEqual([ONE_PAN.name])
    })

    it('refuses a result that is not for the filter asked for', async () => {
      const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: 'j1' }) })
      jobs.j1 = state('succeeded', 'done', [CURRY], true)
      await renderPage()
      await searched(fetchMock)
      expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't read the ideas.")
      expect(screen.queryAllByRole('article')).toHaveLength(0)
    })
  })

  describe('Add to Quest log', () => {
    it('adds the idea and says so on its card', async () => {
      const added = Promise.withResolvers<Response>()
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/1/quest-log': () => added.promise,
      })
      jobs.j1 = state('succeeded', 'done', [CURRY, BOWLS])
      await renderPage()
      await searched(fetchMock)
      await screen.findByText('2 ideas')

      const bowls = within(card(BOWLS.name))
      fireEvent.click(bowls.getByRole('button', { name: 'Add to Quest log' }))
      expect(await bowls.findByRole('button', { name: 'Adding…' })).toBeDisabled()
      added.resolve(jsonResponse(201, dinnerBody('dn-1', BOWLS.name, { status: 'quest_log', origin: 'ingredients' })))
      expect(await bowls.findByRole('status')).toHaveTextContent('In the Quest log')
      expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/1/quest-log')).toHaveLength(1)
      // The other card is untouched.
      expect(within(card(CURRY.name)).getByRole('button', { name: 'Add to Quest log' })).toBeEnabled()
    })

    it('says an idea that came back as an existing Regular is already a Regular, not in the Quest log', async () => {
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/quest-log': () =>
          jsonResponse(200, dinnerBody('dn-reg', 'Coconut Chicken Curry', { status: 'regular' })),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Add to Quest log' }))
      expect(await curry.findByRole('status')).toHaveTextContent('Already a Regular')
      expect(curry.queryByText('In the Quest log')).toBeNull()
    })

    it('fails on a dinner whose status is neither quest_log nor regular', async () => {
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/quest-log': () => jsonResponse(200, dinnerBody('dn-1', CURRY.name, { status: 'suggested' })),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Add to Quest log' }))
      expect(await curry.findByRole('alert')).toHaveTextContent(
        'POST /api/v1/ideas/j1/items/0/quest-log: status "suggested" is not one of quest_log, regular',
      )
      expect(curry.queryByRole('status')).toBeNull()
    })

    it('shows its failure on its card and lets it be pressed again', async () => {
      let fail = true
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/quest-log': () =>
          fail ? errorResponse(500, 'internal') : jsonResponse(200, dinnerBody('dn-1', CURRY.name, { status: 'quest_log' })),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Add to Quest log' }))
      expect(await curry.findByRole('alert')).toHaveTextContent("Couldn't add it to the Quest log. internal message")

      fail = false
      fireEvent.click(curry.getByRole('button', { name: 'Add to Quest log' }))
      expect(await curry.findByRole('status')).toHaveTextContent('In the Quest log')
      expect(curry.queryByRole('alert')).toBeNull()
    })
  })

  describe('Cook tonight', () => {
    it("puts the idea on tonight's plan", async () => {
      const planned = Promise.withResolvers<Response>()
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/cook-tonight': () => planned.promise,
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Cook tonight' }))
      expect(await curry.findByRole('button', { name: 'Planning…' })).toBeDisabled()
      planned.resolve(jsonResponse(200, { dinner: dinnerBody('dn-1', CURRY.name, { status: 'quest_log' }), plan: tonightPlan() }))
      expect(await curry.findByRole('status')).toHaveTextContent("On tonight's plan")
      expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/0/cook-tonight')).toHaveLength(1)
    })

    it('offers Plan this week, not an error, when the week has no plan', async () => {
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/cook-tonight': () => errorResponse(409, 'no_plan'),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Cook tonight' }))
      const link = await curry.findByRole('link', { name: 'Plan this week' })
      expect(curry.getByRole('status')).toHaveTextContent('This week has no plan yet')
      expect(screen.queryByRole('alert')).toBeNull()
      fireEvent.click(link)
      expect(screen.getByText('Plan screen')).toBeInTheDocument()
    })

    it('shows any other failure on its card', async () => {
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: 'j1' }),
        'POST /api/v1/ideas/j1/items/0/cook-tonight': () => errorResponse(409, 'dinner_already_planned'),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)
      const curry = within(await screen.findByRole('article', { name: CURRY.name }))

      fireEvent.click(curry.getByRole('button', { name: 'Cook tonight' }))
      expect(await curry.findByRole('alert')).toHaveTextContent("Couldn't put it on tonight's plan. dinner_already_planned message")
      expect(curry.queryByRole('link', { name: 'Plan this week' })).toBeNull()
      expect(curry.getByRole('button', { name: 'Cook tonight' })).toBeEnabled()
    })
  })

  describe('errors', () => {
    it('shows the ErrorBanner when the search cannot start, and Retry asks again', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => (++n === 1 ? errorResponse(400, 'invalid_request') : jsonResponse(202, { jobId: 'j1' })),
      })
      jobs.j1 = state('succeeded', 'done', [CURRY])
      await renderPage()
      enter(...INGREDIENTS)
      fireEvent.click(findButton())

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't start looking for ideas. invalid_request message")
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('article', { name: CURRY.name })
      expect(bodyOf(calls(fetchMock, FIND)[1]!)).toEqual({ ingredients: INGREDIENTS, quick_only: false })
    })

    it('shows a failed job with no cards, and Retry searches again', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: `j${++n}` }) })
      jobs.j1 = state('failed', 'grounding', [], false, 'ai_failed')
      jobs.j2 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't find ideas. The AI assistant couldn't come up with ideas.")
      expect(screen.queryAllByRole('article')).toHaveLength(0)
      expect(screen.queryAllByTestId('skeleton-card')).toHaveLength(0)

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('article', { name: CURRY.name })
      expect(calls(fetchMock, FIND)).toHaveLength(2)
    })

    it.each([
      ['search_not_configured', 'Recipe search is not set up.'],
      ['search_failed', "The recipe search couldn't be reached."],
    ])('explains %s', async (code, message) => {
      const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: 'j1' }) })
      jobs.j1 = state('failed', 'grounding', [], false, code)
      await renderPage()
      await searched(fetchMock)
      expect(await screen.findByRole('alert')).toHaveTextContent(message)
    })

    it('shows no ideas found with the ErrorBanner and no cards, and Retry searches again', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({ [FIND]: () => jsonResponse(202, { jobId: `j${++n}` }) })
      jobs.j1 = state('failed', 'grounding', [], false, 'no_ideas_found')
      jobs.j2 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't find ideas. No ideas came back with a recipe to match.")
      expect(screen.queryAllByRole('article')).toHaveLength(0)
      expect(screen.queryAllByTestId('skeleton-card')).toHaveLength(0)
      expect(screen.queryByRole('heading', { name: 'No ideas found' })).toBeNull()

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('article', { name: CURRY.name })
      expect(calls(fetchMock, FIND)).toHaveLength(2)
      expect(screen.queryByRole('alert')).toBeNull()
    })

    it('shows an unknown job with the ErrorBanner, and Retry searches again', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({
        [FIND]: () => jsonResponse(202, { jobId: `j${++n}` }),
        'GET /api/v1/jobs/j1': () => errorResponse(404, 'not_found'),
      })
      jobs.j2 = state('succeeded', 'done', [CURRY])
      await renderPage()
      await searched(fetchMock)

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't check on the ideas. not_found message")
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('article', { name: CURRY.name })
      expect(calls(fetchMock, FIND)).toHaveLength(2)
      expect(screen.queryByRole('alert')).toBeNull()
    })
  })
})
