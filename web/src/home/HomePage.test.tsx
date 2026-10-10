import { fireEvent, render, screen, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProviders, HOME } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, jsonResponse, stubFetch, stubFetchByPath } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { HOME_PATH } from './api.ts'
import { HomePage } from './HomePage.tsx'

// NOTHING_YET is the home of a household with no plan, no Side quests, no
// Supply run and nothing unread.
const NOTHING_YET = {
  ...HOME,
  tonight: null,
  sideQuestCount: 0,
  shoppingList: { weekStart: '2026-09-28', label: 'Week 40 · Sep 28 – Oct 4', ready: false, sentToChatAt: null },
  recentUpdates: { unread: false },
}

afterEach(() => {
  vi.unstubAllGlobals()
})

// Where shows the route a link went to.
function Where() {
  const location = useLocation()
  return <p data-testid="where">{location.pathname + location.search}</p>
}

function renderHome(body: unknown) {
  stubFetchByPath({ [HOME_PATH]: () => jsonResponse(200, body) })
  return renderRouter()
}

function renderRouter() {
  const router = createMemoryRouter(
    [
      { path: '/', element: <HomePage /> },
      { path: '*', element: <Where /> },
    ],
    { initialEntries: ['/'] },
  )
  render(
    <ApiProviders>
      <RouterProvider router={router} />
    </ApiProviders>,
  )
}

describe('HomePage', () => {
  it('renders mock 1g from GET /api/v1/home', async () => {
    renderHome(HOME)

    expect(await screen.findByText('Friday, Oct 2')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: "Tonight's quest" })).toBeInTheDocument()

    const tonight = screen.getByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Pizza night' })).toBeInTheDocument()
    expect(within(tonight).getByText('Regular · 40 min')).toBeInTheDocument()
    expect(within(tonight).getByRole('img', { name: 'Pizza night' })).toHaveAttribute('src', '/media/photos/p1-400.jpg')
    expect(within(tonight).getByRole('link', { name: 'Recipe' })).toHaveAttribute('href', '/dinner/d1')

    expect(screen.getByRole('link', { name: 'Quick dinner' })).toHaveAttribute('href', '/quick')
    expect(screen.getByRole('link', { name: 'Cook from what I have' })).toHaveAttribute('href', '/cook-from-pantry')
    expect(screen.getByRole('link', { name: '10 new side quests to swipe' })).toHaveAttribute('href', '/discover')
    expect(screen.getByRole('link', { name: 'Shopping list ready · Oct 5–11' })).toHaveAttribute(
      'href',
      '/plan/shopping?week=2026-10-05',
    )
    expect(screen.queryByRole('heading', { name: 'No quest planned tonight' })).not.toBeInTheDocument()
  })

  it('shows the grape unread dot on the bell when there are unread updates', async () => {
    renderHome(HOME)

    const bell = await screen.findByRole('button', { name: 'Recent updates, unread' })
    const dot = within(bell).getByTestId('unread-dot')
    expect(declaredStyle(dot).background).toBe('var(--color-accent)')
    expect(declaredStyle(dot).width).toBe('8px')
    expectMinHitArea(bell)
  })

  it('shows the bell without a dot when every update is seen', async () => {
    renderHome(NOTHING_YET)

    const bell = await screen.findByRole('button', { name: 'Recent updates' })
    expect(within(bell).queryByTestId('unread-dot')).not.toBeInTheDocument()
  })

  it('labels a Side quest tonight "Side quest"', async () => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, origin: 'side_quest', minutes_total: 30 } })
    expect(await screen.findByText('Side quest · 30 min')).toBeInTheDocument()
  })

  it('shows only the origin when tonight has no minutes', async () => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, minutes_total: null, photo: null } })
    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByText('Regular')).toBeInTheDocument()
    // No photo: the striped placeholder, named for the dinner.
    expect(within(tonight).getByRole('img', { name: 'Pizza night' })).toHaveTextContent('dish photo')
  })

  it('offers to plan the week when tonight has no plan, and hides the rows that do not apply', async () => {
    renderHome(NOTHING_YET)

    expect(await screen.findByRole('heading', { name: 'No quest planned tonight' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /side quests? to swipe/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Shopping list ready/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()
    // The tiles stay.
    expect(screen.getByRole('link', { name: 'Quick dinner' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Plan this week' }))
    expect(await screen.findByTestId('where')).toHaveTextContent('/plan')
  })

  it('hides the Side quests row at zero and keeps a ready Supply run', async () => {
    renderHome({ ...HOME, sideQuestCount: 0 })

    expect(await screen.findByRole('link', { name: 'Shopping list ready · Oct 5–11' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /side quests? to swipe/ })).not.toBeInTheDocument()
  })

  it('hides the Supply run row when no list is ready', async () => {
    renderHome({ ...HOME, shoppingList: NOTHING_YET.shoppingList })

    expect(await screen.findByRole('link', { name: '10 new side quests to swipe' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Shopping list ready/ })).not.toBeInTheDocument()
  })

  it('counts a single Side quest in the singular', async () => {
    renderHome({ ...HOME, sideQuestCount: 1 })
    expect(await screen.findByRole('link', { name: '1 new side quest to swipe' })).toBeInTheDocument()
  })

  it('shows a week spanning two months in full', async () => {
    renderHome({ ...HOME, shoppingList: { ...HOME.shoppingList, weekStart: '2026-09-28', label: 'Week 40 · Sep 28 – Oct 4' } })
    expect(await screen.findByRole('link', { name: 'Shopping list ready · Sep 28–Oct 4' })).toBeInTheDocument()
  })

  it.each([
    ['Recipe', '/dinner/d1'],
    ['Quick dinner', '/quick'],
    ['Cook from what I have', '/cook-from-pantry'],
    ['10 new side quests to swipe', '/discover'],
    ['Shopping list ready · Oct 5–11', '/plan/shopping?week=2026-10-05'],
  ])('%s goes to %s', async (name, to) => {
    renderHome(HOME)

    fireEvent.click(await screen.findByRole('link', { name }))
    expect(await screen.findByTestId('where')).toHaveTextContent(to)
  })

  it('styles the card, tiles and rows as mock 1g with tap targets of at least 44 px', async () => {
    renderHome(HOME)

    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(declaredStyle(tonight).borderRadius).toBe('32px')
    expect(declaredStyle(within(tonight).getByRole('img')).height).toBe('150px')
    expect(declaredStyle(within(tonight).getByRole('heading')).fontSize).toBe('22px')

    const quick = screen.getByRole('link', { name: 'Quick dinner' })
    expect(declaredStyle(quick).height).toBe('104px')
    expect(declaredStyle(quick).background).toBe('var(--color-accent)')
    expect(declaredStyle(quick).color).toBe('var(--on-accent)')
    const pantry = screen.getByRole('link', { name: 'Cook from what I have' })
    expect(declaredStyle(pantry).height).toBe('104px')
    expect(declaredStyle(pantry).background).toBe('var(--color-accent-2-300)')

    expect(declaredStyle(screen.getByText('Friday, Oct 2')).fontSize).toBe('12px')

    expectMinHitArea(within(tonight).getByRole('link', { name: 'Recipe' }))
    // The tiles are 104 px tall (above); the status rows fill the width at 44 px or more.
    for (const row of within(screen.getByRole('list', { name: 'Status' })).getAllByRole('link')) {
      expect(declaredStyle(row).minHeight).toBe('44px')
    }
  })

  it('shows skeletons while the home loads', async () => {
    let answer!: (response: Response) => void
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(() => new Promise((resolve) => (answer = resolve))),
    )
    renderRouter()

    expect(screen.getByTestId('skeleton-tonight')).toBeInTheDocument()
    expect(screen.getByTestId('skeleton-date')).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-status')).toHaveLength(2)
    expect(screen.getByRole('heading', { level: 1, name: "Tonight's quest" })).toBeInTheDocument()

    answer(jsonResponse(200, HOME))
    expect(await screen.findByText('Friday, Oct 2')).toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tonight')).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-status')).not.toBeInTheDocument()
  })

  it('reports a failure with Retry, and no made-up content', async () => {
    stubFetch(errorResponse(500, 'internal'), jsonResponse(200, HOME))
    renderRouter()

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load tonight's quest. internal message")
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'No quest planned tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('heading', { name: 'Pizza night' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('reports a home it cannot read rather than guessing', async () => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, origin: 'leftovers' } })
    expect(await screen.findByRole('alert')).toHaveTextContent('origin "leftovers" is not one of regular, side_quest')
  })
})
