import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { declaredStyle } from '../test/cssRules.ts'
import { dinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { QuickDinnersPage } from './QuickDinnersPage.tsx'
import styles from './QuickDinnersPage.module.css'

const QUICK = 'POST /api/v1/ideas/quick'

// idea is a grounded Quick dinner idea as the job's result carries it.
function idea(name: string, method: string, minutes: number | null, difficulty: string) {
  return {
    name,
    method,
    minutes_total: minutes,
    difficulty,
    quick: true,
    recipe_id: `r-${name}`,
    photo_url: `/media/photos/p-${name.length}-400.jpg`,
    source_url: 'https://recipes.example/x',
    site_name: 'Recipes',
  }
}

// The ideas of mock 1j, and more for later pages and the Crock pot tab.
const ORZO = idea('Lemon garlic shrimp orzo', 'stovetop', 25, 'medium')
const QUESADILLAS = idea('Chicken quesadillas', 'stovetop', 20, 'easy')
const BROCCOLI = idea('Beef & broccoli', 'stovetop', 30, 'easy')
const GNOCCHI = idea('Pesto gnocchi & peas', 'stovetop', 15, 'easy')
const CHILI = idea('Crock pot chili', 'crockpot', 360, 'easy')
const CARNITAS = idea('Pork carnitas', 'crockpot', null, 'medium')

type JobBody = { status: string; result: unknown; error: string | null }

// state is a quick_ideas job's state at stage with ideas.
function state(status: string, stage: string, kind: string, ideas: unknown[], error: string | null = null): JobBody {
  return { status, result: { stage, kind, exclude: [], ideas, dropped: 0 }, error }
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
      return Promise.resolve(jsonResponse(200, { id, kind: 'quick_ideas', ...body }))
    }
    const respond = Object.hasOwn(responders, key) ? responders[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond(init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, jobs }
}

// quickServer answers each POST /ideas/quick with the next of jobIds.
function quickServer(jobIds: string[], responders: Record<string, Responder> = {}) {
  let n = 0
  return stubServer({
    [QUICK]: () => {
      const jobId = jobIds[n++]
      if (jobId === undefined) throw new Error('unexpected quick search')
      return jsonResponse(202, { jobId })
    },
    ...responders,
  })
}

// calls are the requests sent as "METHOD path", in order.
function calls(fetchMock: ReturnType<typeof stubServer>['fetchMock'], key: string) {
  return fetchMock.mock.calls.filter(([input, init]) => `${init?.method ?? 'GET'} ${String(input)}` === key)
}

// searches are the POST /ideas/quick bodies sent, in order.
function searches(fetchMock: ReturnType<typeof stubServer>['fetchMock']) {
  return calls(fetchMock, QUICK).map((call) => JSON.parse(String(call[1]?.body)) as unknown)
}

async function renderPage(queryClient: QueryClient = createQueryClient()) {
  const api = createApiClient(fakeLocation('/quick'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/quick']}>
          <Routes>
            <Route path="/quick" element={<QuickDinnersPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

function tab(name: string) {
  return within(screen.getByRole('radiogroup', { name: 'Kind of Quick dinner' })).getByRole('radio', { name })
}

function row(name: string) {
  return screen.getByRole('listitem', { name })
}

function rowNames() {
  return within(screen.getByRole('list', { name: 'Quick dinners' }))
    .queryAllByRole('heading', { level: 2 })
    .map((heading) => heading.textContent)
}

function addButton(name: string) {
  return within(row(name)).getByRole('button', { name: `Add ${name} to the Quest log` })
}

// showMore waits for Show me more, shown once a page is kept.
function showMore() {
  return screen.findByRole('button', { name: 'Show me more' })
}

const SLOW = { timeout: 3000 }

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('QuickDinnersPage', () => {
  it('renders mock 1j: zap header, tabs with 30 min or less chosen, its helper, and its first page', async () => {
    const { fetchMock, jobs } = quickServer(['j1'])
    jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS, BROCCOLI, GNOCCHI])
    const { container } = await renderPage()

    expect(screen.getByRole('heading', { level: 1, name: 'Quick dinners' })).toBeInTheDocument()
    const zap = container.querySelector(`.${styles.zap}`)!
    expect(declaredStyle(zap).width).toBe('36px')
    expect(declaredStyle(zap).background).toBe('var(--color-accent)')
    expect(zap.querySelector('svg')).toHaveAttribute('stroke-width', '2.75')
    expect(tab('30 min or less')).toBeChecked()
    expect(tab('Crock pot')).not.toBeChecked()
    expect(screen.getByText('Prep to table in 30 minutes or less, picked from your tastes.')).toBeInTheDocument()

    await screen.findByRole('listitem', { name: ORZO.name })
    expect(rowNames()).toEqual([ORZO.name, QUESADILLAS.name, BROCCOLI.name, GNOCCHI.name])
    expect(searches(fetchMock)).toEqual([{ kind: 'under30', exclude: [] }])

    const orzo = within(row(ORZO.name))
    expect(orzo.getByText('25 min · Medium')).toHaveClass(styles.meta!)
    expect(orzo.getByRole('img', { name: ORZO.name })).toHaveAttribute('src', '/media/photos/p-24-400.jpg')
    expect(declaredStyle(row(ORZO.name)).background).toBe('var(--color-neutral-100)')
    expect(declaredStyle(row(ORZO.name)).borderRadius).toBe('24px')
    expectMinHitArea(addButton(ORZO.name))
    expect(addButton(ORZO.name)).toBeEnabled()
    expectMinHitArea(await showMore())
    expect(declaredStyle(await showMore()).color).toBe('var(--color-accent-700)')
  })

  it('fills the rows progressively as the server grounds ideas, adding only once the page is found', async () => {
    const { jobs } = quickServer(['j1'])
    jobs.j1 = state('running', 'suggesting', 'under30', [])
    await renderPage()

    await waitFor(() => expect(screen.getAllByTestId('skeleton-row')).toHaveLength(6))
    expect(screen.getByRole('list', { name: 'Quick dinners' })).toHaveAttribute('aria-busy', 'true')

    jobs.j1 = state('running', 'grounding', 'under30', [ORZO])
    await screen.findByRole('listitem', { name: ORZO.name }, SLOW)
    expect(screen.getAllByTestId('skeleton-row')).toHaveLength(5)
    expect(addButton(ORZO.name)).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Show me more' })).toBeNull()

    jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS])
    await screen.findByRole('listitem', { name: QUESADILLAS.name }, SLOW)
    expect(screen.queryAllByTestId('skeleton-row')).toHaveLength(0)
    expect(addButton(ORZO.name)).toBeEnabled()
    expect(await showMore()).toBeInTheDocument()
    expect(screen.getByRole('list', { name: 'Quick dinners' })).toHaveAttribute('aria-busy', 'false')
  })

  describe('tabs', () => {
    it('searches each tab when first shown, keeps what each found, and says what each finds', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2'])
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI, CARNITAS])
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })

      fireEvent.click(tab('Crock pot'))
      expect(tab('Crock pot')).toBeChecked()
      expect(screen.getByText(/in the crock pot.*picked from your tastes\./)).toBeInTheDocument()
      await screen.findByRole('listitem', { name: CHILI.name })
      expect(rowNames()).toEqual([CHILI.name, CARNITAS.name])
      expect(searches(fetchMock)).toEqual([
        { kind: 'under30', exclude: [] },
        { kind: 'crockpot', exclude: [] },
      ])

      // Each tab's ideas are kept: going back asks for nothing.
      fireEvent.click(tab('30 min or less'))
      expect(rowNames()).toEqual([ORZO.name])
      fireEvent.click(tab('Crock pot'))
      expect(rowNames()).toEqual([CHILI.name, CARNITAS.name])
      expect(calls(fetchMock, QUICK)).toHaveLength(2)
    })

    it('shows a crock-pot idea as "Crock pot · 6 hr · Easy", without a time when the recipe has none', async () => {
      const { jobs } = quickServer(['j1', 'j2'])
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI, CARNITAS])
      await renderPage()
      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })

      expect(within(row(CHILI.name)).getByText('Crock pot · 6 hr · Easy')).toBeInTheDocument()
      expect(within(row(CARNITAS.name)).getByText('Crock pot · Medium')).toBeInTheDocument()
    })

    it('keeps a tab for the session, also when the screen is left and opened again', async () => {
      const { fetchMock, jobs } = quickServer(['j1'])
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      const queryClient = createQueryClient()
      const { unmount } = await renderPage(queryClient)
      await screen.findByRole('listitem', { name: ORZO.name })
      unmount()

      await renderPage(queryClient)
      expect(rowNames()).toEqual([ORZO.name])
      expect(calls(fetchMock, QUICK)).toHaveLength(1)
    })

    it('refuses a result that is not of the tab asked for', async () => {
      const { jobs } = quickServer(['j1'])
      jobs.j1 = state('succeeded', 'done', 'crockpot', [CHILI])
      await renderPage()
      expect(await screen.findByRole('alert')).toHaveTextContent(
        "Couldn't read the Quick dinners. GET /api/v1/jobs/j1: result is not Quick dinner ideas of kind under30",
      )
      expect(screen.queryByRole('listitem', { name: CHILI.name })).toBeNull()
    })
  })

  describe('Add to Quest log', () => {
    it('adds the idea with its "+", which becomes a check, kept across tabs', async () => {
      const added = Promise.withResolvers<Response>()
      const { fetchMock, jobs } = quickServer(['j1', 'j2'], { 'POST /api/v1/ideas/j1/items/1/quest-log': () => added.promise })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI])
      await renderPage()
      await screen.findByRole('listitem', { name: QUESADILLAS.name })

      fireEvent.click(addButton(QUESADILLAS.name))
      await waitFor(() => expect(addButton(QUESADILLAS.name)).toBeDisabled())
      added.resolve(jsonResponse(201, dinnerBody('dn-1', QUESADILLAS.name, { status: 'quest_log', origin: 'quick' })))
      const quesadillas = within(row(QUESADILLAS.name))
      expect(await quesadillas.findByRole('status')).toContainElement(quesadillas.getByRole('img', { name: 'In the Quest log' }))
      expect(quesadillas.queryByRole('button')).toBeNull()
      expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/1/quest-log')).toHaveLength(1)
      // The other row is untouched.
      expect(addButton(ORZO.name)).toBeEnabled()

      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })
      fireEvent.click(tab('30 min or less'))
      expect(within(row(QUESADILLAS.name)).getByRole('img', { name: 'In the Quest log' })).toBeInTheDocument()
    })

    it('labels the check Already a Regular when the idea came back as an existing Regular', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2'], {
        'POST /api/v1/ideas/j1/items/0/quest-log': () => jsonResponse(200, dinnerBody('dn-reg', ORZO.name, { status: 'regular' })),
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI])
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })

      fireEvent.click(addButton(ORZO.name))
      const orzo = within(row(ORZO.name))
      expect(await orzo.findByRole('status')).toContainElement(orzo.getByRole('img', { name: 'Already a Regular' }))
      expect(orzo.queryByRole('img', { name: 'In the Quest log' })).toBeNull()
      expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/0/quest-log')).toHaveLength(1)

      // Kept across tabs as what it is.
      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })
      fireEvent.click(tab('30 min or less'))
      expect(within(row(ORZO.name)).getByRole('img', { name: 'Already a Regular' })).toBeInTheDocument()
    })

    it('marks an add that settles while another tab is shown', async () => {
      const added = Promise.withResolvers<Response>()
      const { fetchMock, jobs } = quickServer(['j1', 'j2'], { 'POST /api/v1/ideas/j1/items/0/quest-log': () => added.promise })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI])
      const queryClient = createQueryClient()
      await renderPage(queryClient)
      await screen.findByRole('listitem', { name: ORZO.name })

      fireEvent.click(addButton(ORZO.name))
      await waitFor(() => expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/0/quest-log')).toHaveLength(1))
      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })
      added.resolve(jsonResponse(201, dinnerBody('dn-1', ORZO.name, { status: 'quest_log', origin: 'quick' })))
      await waitFor(() => expect(queryClient.isMutating()).toBe(0))

      fireEvent.click(tab('30 min or less'))
      expect(within(row(ORZO.name)).getByRole('img', { name: 'In the Quest log' })).toBeInTheDocument()
      expect(addButton(QUESADILLAS.name)).toBeEnabled()
      expect(calls(fetchMock, 'POST /api/v1/ideas/j1/items/0/quest-log')).toHaveLength(1)
    })

    it('shows its failure in its row and lets it be pressed again', async () => {
      let fail = true
      const { jobs } = quickServer(['j1'], {
        'POST /api/v1/ideas/j1/items/0/quest-log': () =>
          fail ? errorResponse(500, 'internal') : jsonResponse(200, dinnerBody('dn-1', ORZO.name, { status: 'quest_log' })),
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })
      const orzo = within(row(ORZO.name))

      fireEvent.click(addButton(ORZO.name))
      expect(await orzo.findByRole('alert')).toHaveTextContent("Couldn't add it to the Quest log. internal message")

      fail = false
      fireEvent.click(addButton(ORZO.name))
      expect(await orzo.findByRole('img', { name: 'In the Quest log' })).toBeInTheDocument()
      expect(orzo.queryByRole('alert')).toBeNull()
    })
  })

  describe('Show me more', () => {
    it('asks for the same kind excluding every name shown, and adds from the page that found it', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2', 'j3'], {
        'POST /api/v1/ideas/j2/items/0/quest-log': () => jsonResponse(201, dinnerBody('dn-1', BROCCOLI.name, { status: 'quest_log' })),
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO, QUESADILLAS])
      jobs.j2 = state('running', 'grounding', 'under30', [BROCCOLI])
      jobs.j3 = state('succeeded', 'done', 'under30', [GNOCCHI])
      await renderPage()
      await screen.findByRole('listitem', { name: QUESADILLAS.name })

      fireEvent.click(await showMore())
      await screen.findByRole('listitem', { name: BROCCOLI.name }, SLOW)
      // The first page stays while the next fills.
      expect(rowNames()).toEqual([ORZO.name, QUESADILLAS.name, BROCCOLI.name])
      expect(screen.getAllByTestId('skeleton-row')).toHaveLength(5)
      expect(screen.queryByRole('button', { name: 'Show me more' })).toBeNull()

      jobs.j2 = state('succeeded', 'done', 'under30', [BROCCOLI])
      await screen.findByRole('button', { name: 'Show me more' }, SLOW)
      fireEvent.click(addButton(BROCCOLI.name))
      expect(await within(row(BROCCOLI.name)).findByRole('img', { name: 'In the Quest log' })).toBeInTheDocument()

      fireEvent.click(await showMore())
      await screen.findByRole('listitem', { name: GNOCCHI.name })
      expect(searches(fetchMock)).toEqual([
        { kind: 'under30', exclude: [] },
        { kind: 'under30', exclude: [ORZO.name, QUESADILLAS.name] },
        { kind: 'under30', exclude: [ORZO.name, QUESADILLAS.name, BROCCOLI.name] },
      ])
      expect(rowNames()).toEqual([ORZO.name, QUESADILLAS.name, BROCCOLI.name, GNOCCHI.name])
    })

    it('says every idea has been shown when a later page finds none', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2'])
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('failed', 'grounding', 'under30', [], 'no_ideas_found')
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })

      fireEvent.click(await showMore())
      expect(await screen.findByRole('status')).toHaveTextContent("That's every Quick dinner for now.")
      expect(screen.queryByRole('alert')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Show me more' })).toBeNull()
      expect(rowNames()).toEqual([ORZO.name])
      expect(calls(fetchMock, QUICK)).toHaveLength(2)
    })

    it('shows a later page that fails with the ErrorBanner, keeping the rows, and Retry asks again', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2', 'j3'])
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('failed', 'grounding', 'under30', [], 'search_failed')
      jobs.j3 = state('succeeded', 'done', 'under30', [QUESADILLAS])
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })

      fireEvent.click(await showMore())
      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't find Quick dinners. The recipe search couldn't be reached.")
      expect(rowNames()).toEqual([ORZO.name])

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: QUESADILLAS.name })
      expect(searches(fetchMock)[2]).toEqual({ kind: 'under30', exclude: [ORZO.name] })
      expect(screen.queryByRole('alert')).toBeNull()
    })
  })

  describe('errors', () => {
    it('shows the ErrorBanner when the search cannot start, and Retry asks again', async () => {
      let n = 0
      const { fetchMock, jobs } = stubServer({
        [QUICK]: () => (++n === 1 ? errorResponse(500, 'internal') : jsonResponse(202, { jobId: 'j1' })),
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      await renderPage()

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't start looking for Quick dinners. internal message")
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: ORZO.name })
      expect(searches(fetchMock)).toEqual([
        { kind: 'under30', exclude: [] },
        { kind: 'under30', exclude: [] },
      ])
    })

    it('keeps a first page that cannot start across a tab switch, asking again only on Retry', async () => {
      let under30 = 0
      const { fetchMock, jobs } = stubServer({
        [QUICK]: (init) => {
          const { kind } = JSON.parse(String(init?.body)) as { kind: string }
          if (kind === 'crockpot') return jsonResponse(202, { jobId: 'j2' })
          return ++under30 === 1 ? errorResponse(500, 'internal') : jsonResponse(202, { jobId: 'j1' })
        },
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI])
      await renderPage()
      await screen.findByRole('alert')

      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })
      fireEvent.click(tab('30 min or less'))

      const alert = screen.getByRole('alert')
      expect(alert).toHaveTextContent("Couldn't start looking for Quick dinners. internal message")
      expect(screen.queryAllByTestId('skeleton-row')).toHaveLength(0)
      expect(searches(fetchMock)).toEqual([
        { kind: 'under30', exclude: [] },
        { kind: 'crockpot', exclude: [] },
      ])

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: ORZO.name })
      expect(screen.queryByRole('alert')).toBeNull()
      expect(calls(fetchMock, QUICK)).toHaveLength(3)
    })

    it('keeps a later page that cannot start across a tab switch, asking again only on Retry', async () => {
      let under30 = 0
      const { fetchMock, jobs } = stubServer({
        [QUICK]: (init) => {
          const { kind } = JSON.parse(String(init?.body)) as { kind: string }
          if (kind === 'crockpot') return jsonResponse(202, { jobId: 'j2' })
          under30++
          return under30 === 2 ? errorResponse(500, 'internal') : jsonResponse(202, { jobId: under30 === 1 ? 'j1' : 'j3' })
        },
      })
      jobs.j1 = state('succeeded', 'done', 'under30', [ORZO])
      jobs.j2 = state('succeeded', 'done', 'crockpot', [CHILI])
      jobs.j3 = state('succeeded', 'done', 'under30', [QUESADILLAS])
      await renderPage()
      await screen.findByRole('listitem', { name: ORZO.name })
      fireEvent.click(await showMore())
      await screen.findByRole('alert')

      fireEvent.click(tab('Crock pot'))
      await screen.findByRole('listitem', { name: CHILI.name })
      fireEvent.click(tab('30 min or less'))

      const alert = screen.getByRole('alert')
      expect(alert).toHaveTextContent("Couldn't start looking for Quick dinners. internal message")
      expect(rowNames()).toEqual([ORZO.name])
      expect(screen.queryByRole('button', { name: 'Show me more' })).toBeNull()
      expect(calls(fetchMock, QUICK)).toHaveLength(3)

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: QUESADILLAS.name })
      expect(searches(fetchMock)[3]).toEqual({ kind: 'under30', exclude: [ORZO.name] })
      expect(screen.queryByRole('alert')).toBeNull()
    })

    it.each([
      ['ai_failed', "The AI assistant couldn't come up with ideas."],
      ['no_ideas_found', 'No ideas came back with a recipe to match.'],
      ['search_not_configured', 'Recipe search is not set up.'],
      ['internal_error', 'Something went wrong on the server.'],
    ])('shows a first page failed with %s with the ErrorBanner and no rows, and Retry searches again', async (code, message) => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2'])
      jobs.j1 = state('failed', 'grounding', 'under30', [], code)
      jobs.j2 = state('succeeded', 'done', 'under30', [ORZO])
      await renderPage()

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent(`Couldn't find Quick dinners. ${message}`)
      expect(screen.queryAllByRole('listitem', { name: /./ })).toHaveLength(0)
      expect(screen.queryAllByTestId('skeleton-row')).toHaveLength(0)
      expect(screen.queryByRole('button', { name: 'Show me more' })).toBeNull()

      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: ORZO.name })
      expect(calls(fetchMock, QUICK)).toHaveLength(2)
      expect(screen.queryByRole('alert')).toBeNull()
    })

    it('shows an unknown job with the ErrorBanner, and Retry searches again', async () => {
      const { fetchMock, jobs } = quickServer(['j1', 'j2'], { 'GET /api/v1/jobs/j1': () => errorResponse(404, 'not_found') })
      jobs.j2 = state('succeeded', 'done', 'under30', [ORZO])
      await renderPage()

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't check on the Quick dinners. not_found message")
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      await screen.findByRole('listitem', { name: ORZO.name })
      expect(calls(fetchMock, QUICK)).toHaveLength(2)
      expect(screen.queryByRole('alert')).toBeNull()
    })
  })
})
