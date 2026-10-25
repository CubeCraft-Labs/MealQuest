import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProviders, HOME } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { recipeBody } from '../test/dinners.ts'
import { errorResponse, jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { HOME_PATH } from './api.ts'
import { HomePage } from './HomePage.tsx'
import { ROTATION_REVIEWS_PATH } from './rotation.ts'
import { clickPath, notificationOptions, parsePushPayload } from '../push/notification.ts'
// The push message the server's Web Push sender delivers for a viewable
// dinner's reminder, byte for byte (internal/push/delivered_test.go).
import deliveredViewable from '../push/delivered/reminder-viewable.json?raw'

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
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

// Where shows the route a link went to.
function Where() {
  const location = useLocation()
  return <p data-testid="where">{location.pathname + location.search}</p>
}

function renderHome(body: unknown) {
  stubFetchByPath({ [HOME_PATH]: () => jsonResponse(200, body), [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }) })
  return renderRouter()
}

function renderRouter(entry = '/', strict = false) {
  const router = createMemoryRouter(
    [
      { path: '/', element: <HomePage /> },
      { path: '*', element: <Where /> },
    ],
    { initialEntries: [entry] },
  )
  const app = (
    <ApiProviders>
      <RouterProvider router={router} />
    </ApiProviders>
  )
  render(strict ? <StrictMode>{app}</StrictMode> : app)
  return router
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
    expect(within(tonight).getByRole('button', { name: 'Recipe' })).toBeInTheDocument()

    expect(screen.getByRole('link', { name: 'Quick dinner' })).toHaveAttribute('href', '/quick')
    expect(screen.getByRole('link', { name: 'Cook from what I have' })).toHaveAttribute('href', '/cook-from-pantry')
    expect(screen.getByRole('link', { name: '10 new side quests to swipe' })).toHaveAttribute('href', '/discover')
    expect(screen.getByRole('link', { name: 'Shopping list ready · Oct 5–11' })).toHaveAttribute(
      'href',
      '/plan/shopping?week=2026-10-05',
    )
    expect(screen.queryByRole('heading', { name: 'No quest planned tonight' })).not.toBeInTheDocument()
  })

  it("reads Home again at the household's next local midnight, so a Home left open moves to the new day", async () => {
    // setTimeout moves with real time, and the test jumps to midnight.
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'], shouldAdvanceTime: true })
    // 23:58 on Friday Oct 2 in New York, the household's zone.
    vi.setSystemTime(new Date('2026-10-03T03:58:00Z'))
    const days = [HOME, { ...HOME, dateLabel: 'Saturday, Oct 3', tonight: null }]
    let reads = 0
    stubFetchByPath({
      [HOME_PATH]: () => jsonResponse(200, days[Math.min(reads++, 1)]),
      [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }),
    })
    renderRouter()
    expect(await screen.findByText('Friday, Oct 2')).toBeInTheDocument()
    expect(reads).toBe(1)

    // A minute before midnight Home is not read again.
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(reads).toBe(1)
    // At midnight it is, and shows the new day.
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(await screen.findByText('Saturday, Oct 3')).toBeInTheDocument()
    expect(reads).toBe(2)
    expect(screen.getByRole('heading', { name: 'No quest planned tonight' })).toBeInTheDocument()
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

  it('Recipe goes to /dinner/d1 once the server answers that Pizza night is there', async () => {
    const fetchMock = stubFetchByPath({
      [HOME_PATH]: () => jsonResponse(200, HOME),
      [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }),
      '/api/v1/dinners/d1': () => jsonResponse(200, recipeBody('d1', 'Pizza night')),
    })
    renderRouter()

    fireEvent.click(await screen.findByRole('button', { name: 'Recipe' }))
    expect(await screen.findByTestId('where')).toHaveTextContent('/dinner/d1')
    const check = fetchMock.mock.calls.find(([input]) => String(input) === '/api/v1/dinners/d1')
    expect(check?.[1]?.cache).toBe('no-store')
  })

  // A tapped notification offering a recipe opens Home asked to open it
  // (src/push/notification.ts recipeOpenPath), never the detail unchecked.
  describe("a notification's recipe", () => {
    function stubRecipe(answer: () => Response) {
      return stubFetchByPath({
        [HOME_PATH]: () => jsonResponse(200, HOME),
        [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }),
        '/api/v1/dinners/d7': answer,
      })
    }

    it.each([false, true])('opens once the server answers that the dinner has it (StrictMode %s)', async (strict) => {
      const fetchMock = stubRecipe(() => jsonResponse(200, recipeBody('d7', 'Tacos')))
      renderRouter('/?recipe=d7&name=Tacos', strict)

      expect(await screen.findByTestId('where')).toHaveTextContent('/dinner/d7')
      const checks = fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/dinners/d7')
      expect(checks.length).toBeGreaterThan(0)
      for (const [, init] of checks) expect(init?.cache).toBe('no-store')
    })

    it('opens nothing for a dinner archived since, says its recipe is gone, and asks only once', async () => {
      const fetchMock = stubRecipe(() => errorResponse(404, 'not_found'))
      const router = renderRouter('/?recipe=d7&name=Tacos')

      expect(await screen.findByText('Tacos was removed, so its recipe is gone.')).toBeInTheDocument()
      expect(screen.queryByTestId('where')).not.toBeInTheDocument()
      expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()
      expect(router.state.location.search).toBe('')
      expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/dinners/d7')).toHaveLength(1)
    })

    it('offers Retry when the server cannot be asked', async () => {
      let reachable = false
      stubRecipe(() => {
        if (!reachable) throw new TypeError('Failed to fetch')
        return jsonResponse(200, recipeBody('d7', 'Tacos'))
      })
      renderRouter('/?recipe=d7&name=Tacos')

      expect(await screen.findByText(/Couldn't check the recipe for Tacos with the server, so it didn't open\./)).toBeInTheDocument()
      reachable = true
      fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
      expect(await screen.findByTestId('where')).toHaveTextContent('/dinner/d7')
    })

    describe("the server's delivered reminder, tapped", () => {
      const origin = 'https://mealquest.example'
      const tapped = clickPath(notificationOptions(parsePushPayload(deliveredViewable, origin).notice).data, origin)
      function stubDelivered(answer: () => Response) {
        return stubFetchByPath({
          [HOME_PATH]: () => jsonResponse(200, HOME),
          [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }),
          '/api/v1/dinners/01TACOS': answer,
        })
      }

      it('opens the recipe of a viewable dinner once the server has it', async () => {
        const fetchMock = stubDelivered(() => jsonResponse(200, recipeBody('01TACOS', 'Tacos')))
        renderRouter(tapped)

        expect(await screen.findByTestId('where')).toHaveTextContent('/dinner/01TACOS')
        const checks = fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/dinners/01TACOS')
        expect(checks.length).toBeGreaterThan(0)
        for (const [, init] of checks) expect(init?.cache).toBe('no-store')
      })

      it('opens nothing when the dinner was archived after the notification was shown', async () => {
        stubDelivered(() => errorResponse(404, 'not_found'))
        renderRouter(tapped)

        expect(await screen.findByText('Tacos was removed, so its recipe is gone.')).toBeInTheDocument()
        expect(screen.queryByTestId('where')).not.toBeInTheDocument()
        expect(screen.queryByText('Dinner not found')).not.toBeInTheDocument()
      })
    })

    it('says so, and asks for nothing, when the link names no dinner', async () => {
      const fetchMock = stubRecipe(() => jsonResponse(200, recipeBody('d7', 'Tacos')))
      renderRouter('/?recipe=d7')

      expect(await screen.findByText("That notification's recipe link names no dinner, so nothing opened.")).toBeInTheDocument()
      expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/dinners/d7')).toHaveLength(0)
      expect(screen.queryByTestId('where')).not.toBeInTheDocument()
    })
  })

  it.each([
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

    expectMinHitArea(within(tonight).getByRole('button', { name: 'Recipe' }))
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
    const homes = [errorResponse(500, 'internal'), jsonResponse(200, HOME)]
    stubFetchByPath({
      [HOME_PATH]: () => homes.shift()!,
      [ROTATION_REVIEWS_PATH]: () => jsonResponse(200, { reviews: [] }),
    })
    renderRouter()

    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load tonight's quest. internal message")
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'No quest planned tonight' })).not.toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('heading', { name: 'Pizza night' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows a dinner archived after the plan was made with no Recipe pill to its missing recipe', async () => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, status: 'archived' } })

    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Pizza night' })).toBeInTheDocument()
    expect(within(tonight).queryByRole('button', { name: 'Recipe' })).not.toBeInTheDocument()
    expect(within(tonight).queryByRole('link')).not.toBeInTheDocument()
  })

  it.each([['an unknown', 'gone'], ['no', undefined]])('reports tonight with %s status rather than guessing it', async (_, status) => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, status } })

    expect(await screen.findByRole('alert')).toHaveTextContent(
      `status ${JSON.stringify(status)} is not one of suggested, quest_log, tried, regular, archived`,
    )
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
  })

  it('reports a home it cannot read rather than guessing', async () => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, origin: 'leftovers' } })
    expect(await screen.findByRole('alert')).toHaveTextContent('origin "leftovers" is not one of regular, side_quest')
  })

  it.each([0, -1, 2.5])('reports tonight with minutes_total %s rather than showing it', async (minutes) => {
    renderHome({ ...HOME, tonight: { ...HOME.tonight, minutes_total: minutes } })

    expect(await screen.findByRole('alert')).toHaveTextContent(
      `tonight's minutes_total ${minutes} is not a positive whole number`,
    )
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByText(/min$/)).not.toBeInTheDocument()
  })

  it('reports tonight without a minutes_total rather than showing it as unknown', async () => {
    const { minutes_total: _, ...tonight } = HOME.tonight
    renderHome({ ...HOME, tonight })

    expect(await screen.findByRole('alert')).toHaveTextContent('tonight has no minutes_total')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.queryByText('Regular')).not.toBeInTheDocument()
  })

  it.each([
    ['a missing', undefined],
    ['an empty', ''],
  ])('reports a ready Supply run with %s label rather than showing it', async (_, label) => {
    renderHome({ ...HOME, shoppingList: { ...HOME.shoppingList, label } })

    expect(await screen.findByRole('alert')).toHaveTextContent('the ready shoppingList has no label')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Shopping list ready/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()
  })

  it('reports a ready Supply run whose label is not its week rather than showing it', async () => {
    renderHome({ ...HOME, shoppingList: { ...HOME.shoppingList, label: 'Week 40 · Sep 28 – Oct 4' } })

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'shoppingList label "Week 40 · Sep 28 – Oct 4" is not the week of 2026-10-05',
    )
    expect(screen.queryByRole('link', { name: /Shopping list ready/ })).not.toBeInTheDocument()
  })

  it.each([
    ['a missing', undefined],
    ['an empty', ''],
  ])('reports an unready Supply run with %s label rather than showing the home', async (_, label) => {
    renderHome({ ...HOME, shoppingList: { ...NOTHING_YET.shoppingList, label } })

    expect(await screen.findByRole('alert')).toHaveTextContent('the unready shoppingList has no label')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()
  })

  it('reports an unready Supply run whose label is not its week rather than showing the home', async () => {
    renderHome({ ...HOME, shoppingList: { ...NOTHING_YET.shoppingList, label: 'Week 41 · Oct 5 – 11' } })

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'shoppingList label "Week 41 · Oct 5 – 11" is not the week of 2026-09-28',
    )
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()
  })

  it('reports a Supply run whose weekStart is not a date rather than showing it', async () => {
    renderHome({ ...HOME, shoppingList: { ...HOME.shoppingList, weekStart: 'Oct 5' } })

    expect(await screen.findByRole('alert')).toHaveTextContent('not a YYYY-MM-DD date')
    expect(screen.queryByRole('list', { name: 'Status' })).not.toBeInTheDocument()
  })
})
