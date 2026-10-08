import { fireEvent, render, screen, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProviders } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { DinnerDetailPage, REGULAR_DINNERS } from './DinnerDetailPage.tsx'

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

// renderAt renders the detail screen at the last of entries, with probes
// for where its links and back button go.
function renderAt(entries: string[]) {
  const router = createMemoryRouter(
    [
      { path: '/dinner/:id', element: <DinnerDetailPage /> },
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

    it('leaves the credit out when the server names no site', async () => {
      serve(detail({ source: { site_name: null } }))
      renderAt(['/dinner/d1'])

      await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
      expect(screen.queryByText(/Recipe from/)).not.toBeInTheDocument()
    })

    it('holds Add to this week and the heart at the bottom, at rest until the next update', async () => {
      serve(detail({ is_favorite: true }))
      renderAt(['/dinner/d1'])

      const add = await screen.findByRole('button', { name: 'Add to this week' })
      expect(add).toHaveAttribute('aria-disabled', 'true')
      expect(add).toHaveAccessibleDescription('Adding to this week and the favorite heart arrive in the next update.')
      const heart = screen.getByRole('button', { name: 'Favorite' })
      expect(heart).toBeDisabled()
      expect(heart).toHaveAttribute('aria-pressed', 'true')
      const bar = heart.parentElement!
      expect(bar).toContainElement(add)
      expect(declaredStyle(bar).position).toBe('sticky')
    })
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
