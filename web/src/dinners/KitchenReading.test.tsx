import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProviders, HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, jsonResponse } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { DinnerDetailPage } from './DinnerDetailPage.tsx'
import { TEXT_SIZE_KEY } from './textSize.ts'

const DINNER = '/api/v1/dinners/d1'
const SETTINGS = '/api/v1/settings/household'
const THIS_WEEK = '/api/v1/plans/2026-10-05'
const NEXT_WEEK = '/api/v1/plans/2026-10-12'

// detail is the Korean beef bowls, a Quest-log Side quest, as GET
// /api/v1/dinners/{id} sends it.
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
    photo_urls: null,
    ingredients: [
      { name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' },
      { name: 'Jasmine rice', qty: '2', unit: 'cups', note: null, raw_text: '2 cups jasmine rice' },
      { name: 'Green onions', qty: '1', unit: 'bunch', note: 'sliced', raw_text: '1 bunch green onions, sliced' },
    ],
    steps: [
      'Rinse the rice and start it cooking.',
      'Brown the beef with the garlic and ginger.',
      'Stir in the sauce and simmer until glossy.',
    ],
    source: null,
    grounding_status: 'grounded',
    grounding_error: null,
    why_picked: 'close cousin of your Beef tacos.',
    ...overrides,
  }
}

// planBody is a week's plan as GET /api/v1/plans/{weekStart} sends it: p0 to
// p6, except the bowls (d1) on each day of bowlsOn, counting from Monday = 0.
function planBody(weekStart: string, ...bowlsOn: number[]) {
  const start = new Date(`${weekStart}T00:00:00Z`).getTime()
  return {
    id: `plan-${weekStart}`,
    week_start: weekStart,
    status: 'draft',
    ai_note: null,
    calendar_synced_at: null,
    days: Array.from({ length: 7 }, (_, i) => ({
      date: new Date(start + i * 86_400_000).toISOString().slice(0, 10),
      dinner:
        bowlsOn.includes(i)
          ? { id: 'd1', name: 'Korean beef bowls', photo: null, minutes: 30, method: 'stovetop' }
          : { id: `p${i}`, name: `Dinner ${i}`, photo: null, minutes: 30, method: 'stovetop' },
      is_new: bowlsOn.includes(i),
      start_by: null,
    })),
  }
}

// serve answers the screen's reads: the dinner, the household settings and
// both weeks' plans, unless responses says otherwise.
function serve(responses: Record<string, () => Response> = {}): Mock<typeof fetch> {
  const all: Record<string, () => Response> = {
    [DINNER]: () => jsonResponse(200, detail()),
    [SETTINGS]: () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    [THIS_WEEK]: () => jsonResponse(200, planBody('2026-10-05', 5)),
    [NEXT_WEEK]: () => errorResponse(404, 'not_found'),
    ...responses,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const path = String(input)
    const respond = Object.hasOwn(all, path) ? all[path] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${path}`)
    return Promise.resolve(respond())
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function reads(fetchMock: Mock<typeof fetch>, path: string) {
  return fetchMock.mock.calls.filter(([input]) => String(input) === path).length
}

// renderPage renders the bowls' detail screen and waits for its title.
async function renderPage() {
  const router = createMemoryRouter([{ path: '/dinner/:id', element: <DinnerDetailPage /> }], {
    initialEntries: ['/dinner/d1'],
  })
  const view = render(
    <ApiProviders>
      <RouterProvider router={router} />
    </ApiProviders>,
  )
  await screen.findByRole('heading', { level: 1, name: 'Korean beef bowls' })
  return view
}

function steps() {
  return within(screen.getByRole('region', { name: 'Steps' })).getAllByRole('listitem')
}

// currentStep is the text of the step marked current.
function currentStep() {
  const current = steps().filter((step) => step.getAttribute('aria-current') === 'step')
  expect(current).toHaveLength(1)
  return current[0]!.textContent
}

// FakeSentinel is a WakeLockSentinel as far as the screen uses one.
type FakeSentinel = EventTarget & { released: boolean; release: Mock<() => Promise<void>> }

// letGo releases a lock as the browser does, telling its listeners so.
function letGo(sentinel: FakeSentinel) {
  if (sentinel.released) return
  sentinel.released = true
  sentinel.dispatchEvent(new Event('release'))
}

// stubWakeLock gives jsdom, which has none, a Screen Wake Lock. Each request
// is granted at once, or, given hold, only when the test calls grant.
function stubWakeLock({ hold = false } = {}) {
  const sentinels: FakeSentinel[] = []
  const pending: (() => void)[] = []
  const request = vi.fn((type: string) => {
    expect(type).toBe('screen')
    const sentinel: FakeSentinel = Object.assign(new EventTarget(), {
      released: false,
      release: vi.fn(() => {
        letGo(sentinel)
        return Promise.resolve()
      }),
    })
    sentinels.push(sentinel)
    if (!hold) return Promise.resolve(sentinel)
    return new Promise<FakeSentinel>((resolve) => pending.push(() => resolve(sentinel)))
  })
  Object.defineProperty(navigator, 'wakeLock', { configurable: true, value: { request } })
  return { request, sentinels, grant: () => pending.shift()!() }
}

// setVisibility shows or hides the page as the browser would, telling the
// page so.
function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => {
  mockViewport(1180)
  // Wednesday Oct 7, noon in New York.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
})

afterEach(() => {
  vi.useRealTimers()
  Reflect.deleteProperty(window, 'matchMedia')
  Reflect.deleteProperty(navigator, 'wakeLock')
  Reflect.deleteProperty(document, 'visibilityState')
  vi.unstubAllGlobals()
})

describe('Kitchen reading mode', () => {
  it('is the tablet layout of the same route: header with back, title, Keep screen on and text size', async () => {
    stubWakeLock()
    serve()
    await renderPage()

    const title = screen.getByRole('heading', { level: 1, name: 'Korean beef bowls' })
    expect(declaredStyle(title).fontSize).toBe('36px')
    expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument()
    // Not the phone's sheet: no photo hero tags.
    expect(screen.queryByText('Easy')).not.toBeInTheDocument()

    const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
    expect(keepOn).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(keepOn)
    expect(keepOn).toHaveAttribute('aria-checked', 'true')

    const sizes = screen.getByRole('radiogroup', { name: 'Text size' })
    expect(within(sizes).getAllByRole('radio').map((radio) => radio.getAttribute('aria-label'))).toEqual([
      'Small text',
      'Medium text',
      'Large text',
    ])
    expect(within(sizes).getByRole('radio', { name: 'Medium text' })).toHaveAttribute('aria-checked', 'true')

    expect(declaredStyle(screen.getByRole('region', { name: 'Ingredients' }).parentElement!).gridTemplateColumns).toBe(
      '400px minmax(0, 1fr)',
    )
    expect(screen.getByText(/close cousin of your Beef tacos/)).toHaveTextContent('Why it was picked: close cousin')
  })

  describe('kicker', () => {
    it('names the day the plan has the dinner on, and Side quest', async () => {
      serve()
      await renderPage()
      expect(await screen.findByText('Saturday, Oct 10 · Side quest')).toBeInTheDocument()
    })

    it("names a day of next week's plan, and Regular, for a Regular", async () => {
      serve({
        [DINNER]: () => jsonResponse(200, detail({ status: 'regular', origin: 'regular' })),
        [THIS_WEEK]: () => jsonResponse(200, planBody('2026-10-05')),
        [NEXT_WEEK]: () => jsonResponse(200, planBody('2026-10-12', 0)),
      })
      await renderPage()
      expect(await screen.findByText('Monday, Oct 12 · Regular')).toBeInTheDocument()
    })

    it("names a day of this week's plan already past when that is the only day", async () => {
      // Today is Wednesday; the bowls were on Monday, and are on no day of next week.
      serve({
        [THIS_WEEK]: () => jsonResponse(200, planBody('2026-10-05', 0)),
        [NEXT_WEEK]: () => jsonResponse(200, planBody('2026-10-12')),
      })
      await renderPage()
      expect(await screen.findByText('Monday, Oct 5 · Side quest')).toBeInTheDocument()
    })

    it('names the first day from today on over a day already past', async () => {
      serve({ [THIS_WEEK]: () => jsonResponse(200, planBody('2026-10-05', 0, 1, 4, 6)) })
      await renderPage()
      expect(await screen.findByText('Friday, Oct 9 · Side quest')).toBeInTheDocument()
      expect(screen.queryByText(/Monday|Tuesday|Sunday/)).not.toBeInTheDocument()
    })

    it('names the latest day already past when every day is past', async () => {
      serve({ [THIS_WEEK]: () => jsonResponse(200, planBody('2026-10-05', 0, 1)) })
      await renderPage()
      expect(await screen.findByText('Tuesday, Oct 6 · Side quest')).toBeInTheDocument()
      expect(screen.queryByText(/Monday/)).not.toBeInTheDocument()
    })

    it('is absent when neither week has a plan', async () => {
      const fetchMock = serve({ [THIS_WEEK]: () => errorResponse(404, 'not_found') })
      await renderPage()
      await waitFor(() => expect(reads(fetchMock, THIS_WEEK)).toBe(1))
      await waitFor(() => expect(reads(fetchMock, NEXT_WEEK)).toBe(1))
      await act(async () => {})
      expect(screen.queryByText(/· Side quest/)).not.toBeInTheDocument()
      expect(screen.queryByText(/Oct/)).not.toBeInTheDocument()
    })

    it('says so when the plan cannot be read, with Retry', async () => {
      let failing = true
      serve({
        [THIS_WEEK]: () => (failing ? errorResponse(500, 'internal') : jsonResponse(200, planBody('2026-10-05', 5))),
      })
      await renderPage()
      expect(await screen.findByText(/Couldn't check the plan for this dinner's day/)).toBeInTheDocument()

      failing = false
      fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
      expect(await screen.findByText('Saturday, Oct 10 · Side quest')).toBeInTheDocument()
    })
  })

  describe('ingredients', () => {
    it('checks ingredients off with 22 px checkboxes, struck through, counting those set out', async () => {
      const fetchMock = serve()
      await renderPage()
      const card = screen.getByRole('region', { name: 'Ingredients' })
      expect(within(card).getByText('0 of 3 set out')).toBeInTheDocument()

      const beef = within(card).getByRole('checkbox', { name: 'Ground beef' })
      expect(declaredStyle(beef).width).toBe('22px')
      expect(declaredStyle(beef).height).toBe('22px')
      expect(within(card).getByRole('checkbox', { name: 'Green onions, sliced' })).not.toBeChecked()

      fireEvent.click(beef)
      expect(beef).toBeChecked()
      expect(declaredStyle(within(card).getByText('Ground beef')).textDecoration).toBe('line-through')
      fireEvent.click(within(card).getByRole('checkbox', { name: 'Jasmine rice' }))
      expect(within(card).getByText('2 of 3 set out')).toBeInTheDocument()

      fireEvent.click(beef)
      expect(beef).not.toBeChecked()
      expect(declaredStyle(within(card).getByText('Ground beef')).textDecoration).toBe('')
      expect(within(card).getByText('1 of 3 set out')).toBeInTheDocument()

      // Local to the screen: nothing is sent.
      expect(fetchMock.mock.calls.filter(([, init]) => (init?.method ?? 'GET') !== 'GET')).toEqual([])
    })

    it('starts afresh on the next visit', async () => {
      serve()
      const view = await renderPage()
      fireEvent.click(screen.getByRole('checkbox', { name: 'Ground beef' }))
      fireEvent.click(screen.getByRole('button', { name: 'Next step' }))
      view.unmount()

      await renderPage()
      expect(screen.getByText('0 of 3 set out')).toBeInTheDocument()
      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')
    })
  })

  describe('steps', () => {
    it('highlights the current step in 22 px text, others 18 px, and moves by Previous and Next step', async () => {
      serve()
      await renderPage()
      const previous = screen.getByRole('button', { name: 'Previous' })
      const next = screen.getByRole('button', { name: 'Next step' })

      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')
      expect(previous).toBeDisabled()
      expect(declaredStyle(steps()[0]!).fontSize).toBe('calc(22px * var(--reading-scale))')
      expect(declaredStyle(steps()[0]!).background).toBe('var(--color-accent-100)')
      expect(declaredStyle(steps()[0]!).boxShadow).toBe('inset 0 0 0 2px var(--color-accent-300)')
      expect(declaredStyle(steps()[1]!).fontSize).toBe('calc(18px * var(--reading-scale))')

      fireEvent.click(next)
      expect(currentStep()).toBe('2Brown the beef with the garlic and ginger.')
      // The step done is muted, its number a check.
      expect(steps()[0]!.textContent).toBe('Rinse the rice and start it cooking.')
      expect(steps()[0]!.querySelector('svg')).not.toBeNull()
      expect(declaredStyle(steps()[0]!).color).toBe('var(--color-neutral-600)')
      expect(declaredStyle(steps()[0]!).fontSize).toBe('calc(18px * var(--reading-scale))')

      fireEvent.click(next)
      expect(currentStep()).toBe('3Stir in the sauce and simmer until glossy.')
      expect(next).toBeDisabled()
      fireEvent.click(next)
      expect(currentStep()).toBe('3Stir in the sauce and simmer until glossy.')

      fireEvent.click(previous)
      expect(currentStep()).toBe('2Brown the beef with the garlic and ginger.')
    })

    it('moves by the ← and → keys, stopping at either end', async () => {
      serve()
      await renderPage()

      fireEvent.keyDown(document.body, { key: 'ArrowRight' })
      expect(currentStep()).toBe('2Brown the beef with the garlic and ginger.')
      fireEvent.keyDown(document.body, { key: 'ArrowRight' })
      fireEvent.keyDown(document.body, { key: 'ArrowRight' })
      expect(currentStep()).toBe('3Stir in the sauce and simmer until glossy.')
      fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
      expect(currentStep()).toBe('2Brown the beef with the garlic and ginger.')
      fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
      fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')

      // From a checked ingredient too.
      const beef = screen.getByRole('checkbox', { name: 'Ground beef' })
      beef.focus()
      fireEvent.keyDown(beef, { key: 'ArrowRight' })
      expect(currentStep()).toBe('2Brown the beef with the garlic and ginger.')
    })

    it('leaves the keys alone in a text field, with a modifier, or in the text size control', async () => {
      serve()
      await renderPage()
      const field = document.createElement('input')
      document.body.append(field)
      field.focus()
      fireEvent.keyDown(field, { key: 'ArrowRight' })
      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')
      field.remove()

      fireEvent.keyDown(document.body, { key: 'ArrowRight', altKey: true })
      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')

      const medium = screen.getByRole('radio', { name: 'Medium text' })
      fireEvent.keyDown(medium, { key: 'ArrowRight' })
      expect(screen.getByRole('radio', { name: 'Large text' })).toHaveAttribute('aria-checked', 'true')
      expect(currentStep()).toBe('1Rinse the rice and start it cooking.')
    })
  })

  describe('text size', () => {
    it('is remembered on this device', async () => {
      serve()
      const view = await renderPage()
      fireEvent.click(screen.getByRole('radio', { name: 'Large text' }))
      expect(localStorage.getItem(TEXT_SIZE_KEY)).toBe('large')
      expect(screen.getByRole('radio', { name: 'Large text' })).toHaveAttribute('aria-checked', 'true')
      view.unmount()

      await renderPage()
      expect(screen.getByRole('radio', { name: 'Large text' })).toHaveAttribute('aria-checked', 'true')
      expect(screen.getByRole('radio', { name: 'Medium text' })).toHaveAttribute('aria-checked', 'false')
      expect(declaredStyle(screen.getByRole('heading', { level: 1 }).closest('header')!.parentElement!).getPropertyValue('--reading-scale')).toBe(
        '1.25',
      )
    })

    it('says so when the size saved is not one MealQuest saved, until one is chosen', async () => {
      localStorage.setItem(TEXT_SIZE_KEY, 'huge')
      serve()
      await renderPage()
      expect(screen.getByText(/Could not read the text size chosen on this device/)).toHaveTextContent('"huge"')
      expect(screen.getByRole('radio', { name: 'Medium text' })).toHaveAttribute('aria-checked', 'true')

      fireEvent.click(screen.getByRole('radio', { name: 'Small text' }))
      expect(screen.queryByText(/Could not read the text size/)).not.toBeInTheDocument()
      expect(localStorage.getItem(TEXT_SIZE_KEY)).toBe('small')
    })
  })

  describe('source credit', () => {
    it.each([
      ['a site and a url', { site_name: 'Serious Eats', url: 'https://www.seriouseats.com/bowls' }, 'Recipe from Serious Eats'],
      ['only a url', { site_name: null, url: 'https://www.seriouseats.com/bowls' }, 'Recipe from www.seriouseats.com'],
    ])('links the recipe page in a new tab for %s', async (_, source, name) => {
      serve({ [DINNER]: () => jsonResponse(200, detail({ source })) })
      await renderPage()
      const credit = screen.getByRole('link', { name })
      expect(credit).toHaveAttribute('href', 'https://www.seriouseats.com/bowls')
      expect(credit).toHaveAttribute('target', '_blank')
      expect(credit).toHaveAttribute('rel', 'noopener noreferrer')
    })

    it.each([
      ['only a site', { site_name: 'Serious Eats', url: null }, 'Recipe from Serious Eats'],
      ['neither site nor url', { site_name: null, url: null }, 'Recipe from the web'],
    ])('is plain text for %s', async (_, source, text) => {
      serve({ [DINNER]: () => jsonResponse(200, detail({ source })) })
      await renderPage()
      expect(screen.getByText(text).closest('a')).toBeNull()
      expect(screen.queryByRole('link', { name: /Recipe from/ })).not.toBeInTheDocument()
    })

    it('is absent for a recipe typed in', async () => {
      serve({ [DINNER]: () => jsonResponse(200, detail({ source: null, grounding_status: 'manual' })) })
      await renderPage()
      expect(screen.queryByText(/Recipe from/)).not.toBeInTheDocument()
    })
  })

  it('shows what stands in for a recipe not found, with no step buttons', async () => {
    serve({
      [DINNER]: () =>
        jsonResponse(200, detail({ ingredients: [], steps: [], grounding_status: 'failed', grounding_error: 'no_recipe_found' })),
    })
    await renderPage()
    expect(screen.getByRole('link', { name: 'Fix recipe' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Ingredients' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Next step' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add to this week' })).toBeInTheDocument()
  })

  describe('Keep screen on', () => {
    it('holds a screen wake lock while on and releases it when turned off', async () => {
      const wakeLock = stubWakeLock()
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      expect(keepOn).toBeEnabled()
      expect(wakeLock.request).not.toHaveBeenCalled()

      fireEvent.click(keepOn)
      expect(keepOn).toHaveAttribute('aria-checked', 'true')
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(1))
      expect(wakeLock.sentinels[0]!.release).not.toHaveBeenCalled()

      fireEvent.click(keepOn)
      expect(keepOn).toHaveAttribute('aria-checked', 'false')
      expect(wakeLock.sentinels[0]!.release).toHaveBeenCalledTimes(1)

      // On again: a fresh lock.
      fireEvent.click(keepOn)
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(2))
      expect(wakeLock.sentinels[1]!.released).toBe(false)
    })

    it('releases the lock when the screen is left', async () => {
      const wakeLock = stubWakeLock()
      serve()
      const view = await renderPage()
      fireEvent.click(screen.getByRole('switch', { name: 'Keep screen on' }))
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(1))

      view.unmount()
      expect(wakeLock.sentinels[0]!.release).toHaveBeenCalledTimes(1)
    })

    it('releases a lock granted only after the screen was left', async () => {
      const wakeLock = stubWakeLock({ hold: true })
      serve()
      const view = await renderPage()
      fireEvent.click(screen.getByRole('switch', { name: 'Keep screen on' }))
      expect(wakeLock.request).toHaveBeenCalledTimes(1)

      view.unmount()
      await act(async () => wakeLock.grant())
      expect(wakeLock.sentinels[0]!.release).toHaveBeenCalledTimes(1)
    })

    it('asks for the lock again when the page is visible again while on, and not while off', async () => {
      const wakeLock = stubWakeLock()
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      fireEvent.click(keepOn)
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(1))

      // Hidden, the browser lets the lock go; nothing is asked while hidden.
      act(() => setVisibility('hidden'))
      act(() => letGo(wakeLock.sentinels[0]!))
      expect(wakeLock.request).toHaveBeenCalledTimes(1)

      act(() => setVisibility('visible'))
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(2))
      expect(keepOn).toHaveAttribute('aria-checked', 'true')

      // Visible again with the lock still held: no second lock.
      act(() => setVisibility('visible'))
      expect(wakeLock.request).toHaveBeenCalledTimes(2)

      fireEvent.click(keepOn)
      act(() => setVisibility('hidden'))
      act(() => setVisibility('visible'))
      expect(wakeLock.request).toHaveBeenCalledTimes(2)
    })

    it('asks for the lock again when the browser lets it go while the page is visible', async () => {
      const wakeLock = stubWakeLock()
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      fireEvent.click(keepOn)
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(1))

      act(() => letGo(wakeLock.sentinels[0]!))
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(2))
      await act(async () => {})
      expect(keepOn).toHaveAttribute('aria-checked', 'true')

      // The fresh lock is watched too.
      act(() => letGo(wakeLock.sentinels[1]!))
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(3))
      expect(wakeLock.sentinels[2]!.released).toBe(false)
      expect(keepOn).toHaveAttribute('aria-checked', 'true')
    })

    it('asks for the lock again when the page was hidden and shown while a request was pending', async () => {
      const wakeLock = stubWakeLock({ hold: true })
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      fireEvent.click(keepOn)
      expect(wakeLock.request).toHaveBeenCalledTimes(1)

      // Hidden and shown again before the first lock is granted, which the
      // browser has let go by the time it is.
      act(() => setVisibility('hidden'))
      act(() => setVisibility('visible'))
      expect(wakeLock.request).toHaveBeenCalledTimes(1)
      letGo(wakeLock.sentinels[0]!)
      await act(async () => wakeLock.grant())
      expect(wakeLock.request).toHaveBeenCalledTimes(2)

      await act(async () => wakeLock.grant())
      expect(wakeLock.sentinels[1]!.released).toBe(false)
      expect(wakeLock.request).toHaveBeenCalledTimes(2)
      expect(keepOn).toHaveAttribute('aria-checked', 'true')
    })

    it('reports a lock the browser refuses and turns the switch back off', async () => {
      const wakeLock = stubWakeLock()
      wakeLock.request.mockRejectedValueOnce(new DOMException('Blocked by permissions policy', 'NotAllowedError'))
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      fireEvent.click(keepOn)

      expect(await screen.findByRole('alert')).toHaveTextContent(
        "Couldn't keep the screen on: NotAllowedError: Blocked by permissions policy",
      )
      expect(keepOn).toHaveAttribute('aria-checked', 'false')

      fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
      expect(keepOn).toHaveAttribute('aria-checked', 'true')
      await waitFor(() => expect(wakeLock.request).toHaveBeenCalledTimes(2))
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    })

    it('is disabled, and says why, in a browser that cannot keep the screen on', async () => {
      serve()
      await renderPage()
      const keepOn = screen.getByRole('switch', { name: 'Keep screen on' })
      expect(keepOn).toBeDisabled()
      expect(keepOn).toHaveAttribute('aria-checked', 'false')
      expect(keepOn).toHaveAccessibleDescription(
        "This browser can't keep the screen on, so it may dim while you cook.",
      )
      fireEvent.click(keepOn)
      expect(keepOn).toHaveAttribute('aria-checked', 'false')
    })
  })
})
