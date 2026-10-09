import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { AppShell } from '../shell/AppShell.tsx'
import { cacheFallbackHeader } from '../sw/routes.ts'
import { ApiProviders } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { GROUNDING_POLL_MS } from './api.ts'
import { DinnerDetailPage, REGULAR_DINNERS } from './DinnerDetailPage.tsx'
import sourceCases from './testdata/source_url_cases.json'

const DINNER = '/api/v1/dinners/d1'

// detail is a dinner as GET /api/v1/dinners/{id} sends it: mock 1k's
// Korean beef bowls, a Side quest grounded in a page that named its site.
function detail(overrides: Record<string, unknown> = {}) {
  return {
    id: 'd1',
    name: 'Korean beef bowls',
    status: 'quest_log',
    origin: 'suggested',
    method: 'stovetop',
    minutes_total: 30,
    difficulty: 'easy',
    cuisine: 'korean',
    is_favorite: false,
    photo_urls: { '400': '/media/photos/p1-400.jpg', '1200': '/media/photos/p1-1200.jpg' },
    ingredients: [
      { name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' },
      { name: 'Jasmine rice', qty: '2', unit: 'cups', note: null, raw_text: '2 cups jasmine rice' },
      { name: 'Soy sauce', qty: '¼', unit: 'cup', note: null, raw_text: '¼ cup soy sauce' },
      { name: 'Green onions', qty: '1', unit: 'bunch', note: 'sliced', raw_text: '1 bunch green onions, sliced' },
    ],
    steps: ['Cook the rice.', 'Brown the beef with the sauce.', 'Serve over rice with the onions.'],
    source: { site_name: 'Serious Eats' },
    grounding_status: 'grounded',
    grounding_error: null,
    why_picked: 'close cousin of your Beef tacos and Chicken stir-fry.',
    ...overrides,
  }
}

// A recipe the household typed in: only raw_text, no source.
const MANUAL = {
  status: 'regular',
  origin: 'regular',
  photo_urls: null,
  ingredients: [
    { name: null, qty: null, unit: null, note: null, raw_text: '2 lb chuck roast' },
    { name: null, qty: null, unit: null, note: null, raw_text: 'A splash of red wine' },
  ],
  steps: ['Sear the roast.', 'Slow cook 8 hours.'],
  source: null,
  grounding_status: 'manual',
  why_picked: null,
}

// A Regular whose recipe could not be found: no recipe at all.
const FAILED = {
  status: 'regular',
  origin: 'regular',
  photo_urls: null,
  minutes_total: null,
  difficulty: null,
  ingredients: [],
  steps: [],
  source: null,
  grounding_status: 'failed',
  grounding_error: 'no_recipe_found',
  why_picked: null,
}

// renderAt renders the detail screen at the last of entries, inside the
// app shell's scroll area as App routes it, with probes for where its links
// and back button go.
function renderAt(entries: string[]) {
  const router = createMemoryRouter(
    [
      { element: <AppShell />, children: [{ path: '/dinner/:id', element: <DinnerDetailPage /> }] },
      { path: '/', element: <h1>Home probe</h1> },
      { path: '/plan', element: <h1>Plan probe</h1> },
      { path: REGULAR_DINNERS, element: <h1>Regular dinners probe</h1> },
    ],
    { initialEntries: entries, initialIndex: entries.length - 1 },
  )
  render(
    <ApiProviders>
      <RouterProvider router={router} />
    </ApiProviders>,
  )
  return router
}

function serve(body: unknown) {
  return stubFetchByPath({ [DINNER]: () => jsonResponse(200, body) })
}

beforeEach(() => {
  mockViewport(390)
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

describe('DinnerDetailPage', () => {
  describe('a grounded Side quest', () => {
    it('shows its photo washed and full-bleed, tags, title and why it was picked', async () => {
      serve(detail())
      renderAt(['/dinner/d1'])

      expect(await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })).toBeInTheDocument()
      const photo = screen.getByRole('img', { name: 'Korean beef bowls' })
      expect(photo).toHaveAttribute('src', '/media/photos/p1-400.jpg')
      expect(photo).toHaveAttribute('srcset', '/media/photos/p1-400.jpg 400w, /media/photos/p1-1200.jpg 1200w')
      expect(declaredStyle(photo.parentElement!).height).toBe('250px')
      expect(declaredStyle(photo.parentElement!).margin).toBe('0px -20px')

      expect(screen.getByText('Side quest')).toBeInTheDocument()
      expect(screen.getByText('Easy')).toBeInTheDocument()
      expect(screen.getByText('30 min')).toBeInTheDocument()
      expect(screen.queryByText('Regular')).not.toBeInTheDocument()
      expect(screen.getByText(/close cousin of your Beef tacos/)).toHaveTextContent(
        'Why it was picked: close cousin of your Beef tacos and Chicken stir-fry.',
      )
    })

    it('lists ingredients in recipe order with quantities on the right, then numbered steps', async () => {
      serve(detail())
      renderAt(['/dinner/d1'])

      expect(await screen.findByRole('heading', { level: 2, name: 'Ingredients' })).toBeInTheDocument()
      const rows = within(screen.getAllByRole('list')[0]!).getAllByRole('listitem')
      expect(rows.map((row) => Array.from(row.children, (cell) => cell.textContent))).toEqual([
        ['Ground beef', '1 lb'],
        ['Jasmine rice', '2 cups'],
        ['Soy sauce', '¼ cup'],
        ['Green onions, sliced', '1 bunch'],
      ])
      const qty = rows[0]!.lastElementChild!
      expect(declaredStyle(qty).textAlign).toBe('right')
      expect(declaredStyle(rows[0]!).justifyContent).toBe('space-between')
      expect(declaredStyle(rows[0]!).borderBottom).toBe('1px solid var(--color-divider)')

      expect(screen.getByRole('heading', { level: 2, name: 'Steps' })).toBeInTheDocument()
      const steps = screen.getAllByRole('list')[1]!
      expect(steps.tagName).toBe('OL')
      expect(within(steps).getAllByRole('listitem').map((step) => step.textContent)).toEqual([
        '1Cook the rice.',
        '2Brown the beef with the sauce.',
        '3Serve over rice with the onions.',
      ])
    })

    it('credits the site the recipe came from, with no link to it', async () => {
      serve(detail())
      renderAt(['/dinner/d1'])

      const credit = await screen.findByText('Recipe from Serious Eats')
      expect(credit.closest('a')).toBeNull()
      expect(screen.queryByRole('link', { name: /Recipe from/ })).not.toBeInTheDocument()
    })

    it('links the credit to the recipe in a new tab once the server sends a safe url', async () => {
      serve(detail({ source: { site_name: 'Serious Eats', url: 'https://www.seriouseats.com/korean-beef-bowls' } }))
      renderAt(['/dinner/d1'])

      const credit = await screen.findByRole('link', { name: 'Recipe from Serious Eats' })
      expect(credit).toHaveAttribute('href', 'https://www.seriouseats.com/korean-beef-bowls')
      expect(credit).toHaveAttribute('target', '_blank')
      expect(credit).toHaveAttribute('rel', 'noopener noreferrer')
    })

    it.each([
      ['http', 'http://www.seriouseats.com/korean-beef-bowls'],
      ['a password', 'https://user:secret@www.seriouseats.com/korean-beef-bowls'],
      ['a query', 'https://www.seriouseats.com/korean-beef-bowls?token=abc'],
      ['a fragment', 'https://www.seriouseats.com/korean-beef-bowls#token=abc'],
    ])('refuses a source url with %s', async (_, url) => {
      serve(detail({ source: { site_name: 'Serious Eats', url } }))
      renderAt(['/dinner/d1'])

      expect(await screen.findByRole('alert')).toHaveTextContent('source url is not an https link')
      expect(screen.queryByRole('link', { name: /Recipe from/ })).not.toBeInTheDocument()
    })

    // Each source exactly as the Go handler sends it for a stored link
    // (internal/server's TestGetDinnerSourceURLOnlyWhenSafe checks the same
    // file), so every url the server sends must parse here.
    it.each(sourceCases.cases)('renders the source the server sends for $stored', async ({ source }) => {
      serve(detail({ source }))
      renderAt(['/dinner/d1'])

      const credit = await screen.findByText('Recipe from Curry House')
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      if (source.url === null) {
        expect(credit.closest('a')).toBeNull()
        expect(screen.queryByRole('link', { name: /Recipe from/ })).not.toBeInTheDocument()
      } else {
        expect(screen.getByRole('link', { name: 'Recipe from Curry House' })).toHaveAttribute('href', new URL(source.url).href)
      }
    })

    it("links the credit by the url's host when the server names no site", async () => {
      serve(detail({ source: { site_name: null, url: 'https://Kitchen.Example:8443/korean-beef-bowls' } }))
      renderAt(['/dinner/d1'])

      const credit = await screen.findByRole('link', { name: 'Recipe from kitchen.example:8443' })
      expect(credit).toHaveAttribute('href', 'https://kitchen.example:8443/korean-beef-bowls')
      expect(credit).toHaveAttribute('target', '_blank')
      expect(credit).toHaveAttribute('rel', 'noopener noreferrer')
    })

    // The server withholds both when the stored name could be the link's
    // host and the link could carry a token: the credit names neither.
    it('credits the web in plain text when the server sends neither site nor url', async () => {
      serve(detail({ source: { site_name: null, url: null } }))
      renderAt(['/dinner/d1'])

      const credit = await screen.findByText('Recipe from the web')
      expect(credit.closest('a')).toBeNull()
      expect(screen.queryByRole('link', { name: /Recipe from/ })).not.toBeInTheDocument()
    })

    it('holds Add to this week and the heart at the bottom', async () => {
      serve(detail({ is_favorite: true }))
      renderAt(['/dinner/d1'])

      const add = await screen.findByRole('button', { name: 'Add to this week' })
      expect(add).toBeEnabled()
      const heart = screen.getByRole('button', { name: 'Favorite' })
      expect(heart).toBeEnabled()
      expect(heart).toHaveAttribute('aria-pressed', 'true')
      const bar = heart.parentElement!
      expect(bar).toContainElement(add)
      expect(declaredStyle(bar).position).toBe('sticky')
      expect(declaredStyle(bar).bottom).toBe('0px')
    })
  })

  // jsdom does no layout, so no test here can measure where the bar lands.
  // This pins the CSS chain that puts it at the bottom of the screen on a
  // short recipe: the shell's scroll area is a full-height flex item, the
  // page fills it as a column, the sheet takes the room left and the bar is
  // the page's last child. Drop any link and the bar sits right after the
  // content again, and this fails.
  it('fills the scroll area so the bar rests at the bottom of a short recipe', async () => {
    serve(detail(FAILED))
    renderAt(['/dinner/d1'])

    const bar = (await screen.findByRole('button', { name: 'Favorite' })).parentElement!
    const page = bar.parentElement!
    const main = page.parentElement!
    const shell = main.parentElement!
    expect(main.tagName).toBe('MAIN')
    expect(declaredStyle(shell).height).toBe('100dvh')
    expect(declaredStyle(shell).display).toBe('flex')
    expect(declaredStyle(shell).alignItems).toBe('')
    expect(declaredStyle(main).overflowY).toBe('auto')

    expect(declaredStyle(page).minHeight).toBe('100%')
    expect(declaredStyle(page).display).toBe('flex')
    expect(declaredStyle(page).flexDirection).toBe('column')
    expect(page.lastElementChild).toBe(bar)
    const sheet = screen.getByRole('article')
    expect(sheet.parentElement).toBe(page)
    expect(sheet.nextElementSibling).toBe(bar)
    expect(declaredStyle(sheet).flexGrow).toBe('1')
    expect(declaredStyle(bar).position).toBe('sticky')
  })

  it('shows the striped placeholder when the dinner has no photo', async () => {
    serve(detail({ photo_urls: null }))
    renderAt(['/dinner/d1'])

    const placeholder = await screen.findByRole('img', { name: 'Korean beef bowls' })
    expect(placeholder.tagName).toBe('DIV')
    expect(placeholder).toHaveTextContent('dish photo')
  })

  it('shows a manual Regular line by line, as typed, with no credit', async () => {
    serve(detail(MANUAL))
    renderAt(['/dinner/d1'])

    expect(await screen.findByText('Regular')).toBeInTheDocument()
    expect(screen.queryByText('Side quest')).not.toBeInTheDocument()
    const [ingredients, steps] = screen.getAllByRole('list')
    expect(within(ingredients!).getAllByRole('listitem').map((row) => row.firstElementChild!.textContent)).toEqual([
      '2 lb chuck roast',
      'A splash of red wine',
    ])
    expect(within(steps!).getAllByRole('listitem')).toHaveLength(2)
    expect(screen.queryByText(/Why it was picked/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Recipe from/)).not.toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Korean beef bowls' })).toHaveTextContent('dish photo')
  })

  it('links a dinner whose recipe was not found to Settings → Regular dinners, with no recipe', async () => {
    serve(detail(FAILED))
    const router = renderAt(['/dinner/d1'])

    const fix = await screen.findByRole('link', { name: 'Fix recipe' })
    expect(screen.getByText(/couldn't find a recipe/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Ingredients' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Steps' })).not.toBeInTheDocument()
    expect(screen.queryByRole('list')).not.toBeInTheDocument()

    fireEvent.click(fix)
    expect(await screen.findByRole('heading', { name: 'Regular dinners probe' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe(REGULAR_DINNERS)
  })

  it('says it is still finding a recipe while grounding is pending', async () => {
    serve(detail({ ...FAILED, grounding_status: 'pending', grounding_error: null }))
    renderAt(['/dinner/d1'])

    expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Fix recipe' })).not.toBeInTheDocument()
  })

  describe('while the recipe is being found', () => {
    const PENDING = detail({ ...FAILED, grounding_status: 'pending', grounding_error: null })
    const GROUNDED = detail({ grounding_status: 'grounded' })
    let onLine: boolean
    let body: unknown
    let fromCache: boolean
    let fetchMock: Mock<typeof fetch>

    const reads = () => fetchMock.mock.calls.length
    const tick = (intervals = 1) => act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * intervals))

    function setOnline(value: boolean) {
      onLine = value
      act(() => {
        window.dispatchEvent(new Event(value ? 'online' : 'offline'))
      })
    }

    beforeEach(() => {
      // Only intervals are faked: TanStack Query polls with setInterval, and
      // Testing Library's waits keep their real timers.
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      onLine = true
      vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => onLine)
      body = PENDING
      fromCache = false
      fetchMock = stubFetchByPath({
        [DINNER]: () =>
          new Response(JSON.stringify(body), {
            headers: { 'Content-Type': 'application/json', ...(fromCache ? { [cacheFallbackHeader]: '1' } : {}) },
          }),
      })
    })

    // An offline event leaves TanStack Query's online manager, shared by
    // every test, offline until an online event.
    afterEach(() => {
      window.dispatchEvent(new Event('online'))
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    it('reads the dinner again until the recipe lands, then stops', async () => {
      renderAt(['/dinner/d1'])
      expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
      expect(reads()).toBe(1)

      await tick()
      await waitFor(() => expect(reads()).toBe(2))

      body = GROUNDED
      await tick()
      expect(await screen.findByRole('heading', { level: 2, name: 'Ingredients' })).toBeInTheDocument()
      expect(reads()).toBe(3)

      await tick(3)
      expect(reads()).toBe(3)
    })

    it('pauses while the browser is offline and resumes when it is back', async () => {
      renderAt(['/dinner/d1'])
      expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
      expect(reads()).toBe(1)

      setOnline(false)
      await tick(3)
      expect(reads()).toBe(1)

      setOnline(true)
      await tick()
      await waitFor(() => expect(reads()).toBe(2))
    })

    it('reads a saved pending copy offline once, then waits for the connection', async () => {
      onLine = false
      fromCache = true
      renderAt(['/dinner/d1'])
      expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
      expect(reads()).toBe(1)

      await tick(3)
      expect(reads()).toBe(1)

      // The connection comes back: the saved copy is read again at once,
      // and polling goes on once the server answers.
      fromCache = false
      setOnline(true)
      await waitFor(() => expect(reads()).toBe(2))
      await tick()
      await waitFor(() => expect(reads()).toBe(3))
    })

    it('stops polling on a saved copy even while the browser says it is online', async () => {
      fromCache = true
      renderAt(['/dinner/d1'])
      expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()

      await tick(3)
      expect(reads()).toBe(1)
    })

    it('stops polling when the page is left', async () => {
      const router = renderAt(['/dinner/d1'])
      expect(await screen.findByText(/Still finding the recipe/)).toBeInTheDocument()
      expect(reads()).toBe(1)

      await act(() => router.navigate('/'))
      expect(screen.getByRole('heading', { name: 'Home probe' })).toBeInTheDocument()
      await tick(3)
      expect(reads()).toBe(1)
    })
  })

  it('says an unknown or removed dinner is not found', async () => {
    stubFetchByPath({ [DINNER]: () => errorResponse(404, 'not_found') })
    renderAt(['/dinner/d1'])

    expect(await screen.findByRole('heading', { name: 'Dinner not found' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go Home' })).toHaveAttribute('href', '/')
  })

  it('reports any other failure with a retry', async () => {
    stubFetchByPath({ [DINNER]: () => errorResponse(500, 'internal') })
    renderAt(['/dinner/d1'])

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load this dinner.")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  describe('the back button', () => {
    it('goes back in the app', async () => {
      serve(detail())
      const router = renderAt(['/plan', '/dinner/d1'])

      fireEvent.click(await screen.findByRole('button', { name: 'Back' }))
      expect(await screen.findByRole('heading', { name: 'Plan probe' })).toBeInTheDocument()
      expect(router.state.location.pathname).toBe('/plan')
    })

    it('goes Home when the dinner was opened from a link', async () => {
      serve(detail())
      const router = renderAt(['/dinner/d1'])

      fireEvent.click(await screen.findByRole('button', { name: 'Back' }))
      expect(await screen.findByRole('heading', { name: 'Home probe' })).toBeInTheDocument()
      expect(router.state.location.pathname).toBe('/')
    })
  })
})
