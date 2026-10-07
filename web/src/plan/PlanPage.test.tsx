import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { cacheFallbackHeader } from '../sw/routes.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { PlanPage } from './PlanPage.tsx'

const THIS_WEEK = '/api/v1/plans/2026-10-05'
const NEXT_WEEK = '/api/v1/plans/2026-10-12'
const PREVIOUS_WEEK = '/api/v1/plans/2026-09-28'
const DRAFT = `${THIS_WEEK}/draft`
const JOB = '/api/v1/jobs/j1'

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const NAMES = ['Beef tacos', 'Spaghetti bolognese', 'Crock pot tikka masala', 'Chicken stir-fry', 'Pizza night', 'Korean beef bowls', 'Sheet-pan salmon']

// planBody is the plan of the week of Oct 5 as GET /api/v1/plans/{weekStart}
// sends it: Wednesday's crock-pot tikka masala and Saturday's bowls are
// Quest-log picks, and the tikka masala must start by 1:30 pm New York time
// to be ready for 5:30 pm dinner. Monday's tacos have a stored photo.
function planBody(status: string, names = NAMES) {
  return {
    id: 'plan-1',
    week_start: '2026-10-05',
    week_label: 'Week 41 · Oct 5 – 11',
    status,
    ai_note: 'I mixed 5 of your regulars with 2 from your Quest log. Tap any day to swap it.',
    calendar_synced_at: status === 'draft' ? null : '2026-10-04T15:00:00Z',
    days: DAYS.map((date, i) => ({
      date,
      dinner: {
        id: `d${i}`,
        name: names[i],
        photo: i === 0 ? '/media/photos/tacos-400.jpg' : null,
        minutes: i === 2 ? 240 : 30,
        method: i === 2 ? 'crockpot' : 'stovetop',
        origin: i === 2 || i === 5 ? 'side_quest' : 'regular',
      },
      is_new: i === 2 || i === 5,
      start_by: i === 2 ? '2026-10-07T13:30:00-04:00' : null,
    })),
    updated_at: '2026-10-04T15:00:00Z',
  }
}

// draftJob is job j1 drafting the week of Oct 5 into plan planId: a
// redraft keeps the plan's id.
function draftJob(status: string, error: string | null = null, planId = 'plan-1') {
  const result = status === 'succeeded' ? { week_start: '2026-10-05', plan_id: planId } : null
  return { id: 'j1', kind: 'draft_week', status, result, error }
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

const QUEST_LOG = '/api/v1/dinners?status=quest_log'
const REGULARS = '/api/v1/dinners?status=regular'

// dinner is a dinner as the dinners API lists it.
function dinner(id: string, name: string, difficulty: string | null, minutes: number | null) {
  return {
    id,
    name,
    method: 'stovetop',
    minutes_total: minutes,
    difficulty,
    is_favorite: false,
    photo_url: null,
    grounding_status: 'grounded',
  }
}

// The Quest log has lemon orzo; the Regulars are Monday's tacos, already
// this week, and fish tacos.
const lists = {
  [`GET ${QUEST_LOG}`]: () => jsonResponse(200, { dinners: [dinner('q1', 'Lemon orzo', 'easy', 25)] }),
  [`GET ${REGULARS}`]: () =>
    jsonResponse(200, { dinners: [dinner('d0', 'Beef tacos', 'easy', 30), dinner('r1', 'Fish tacos', 'medium', 35)] }),
}

// stubServer answers "METHOD path" requests from responders, with /me and
// the household settings; any other request fails the test.
function stubServer(responders: Record<string, Responder>) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
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

// posts lists the POSTs sent, as "path body".
function posts(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === 'POST')
    .map(([input, init]) => `${String(input)} ${String(init?.body)}`)
}

// planReads counts the reads of this week's plan.
function planReads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === THIS_WEEK).length
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the Plan page.
async function renderPage() {
  const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter initialEntries={['/plan']}>
          <PlanPage />
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

// Wednesday Oct 7, noon in New York: before Friday's 09:00 plan_draft, so
// the page opens on this week.
beforeEach(() => {
  mockViewport(390)
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

function tile(kicker: string) {
  return screen.getByText(kicker).closest('li')!
}

describe('PlanPage', () => {
  it('renders mock 1f for a draft: title, Draft tag, the assistant note and the 2-column grid', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')) })
    await renderPage()

    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    const title = screen.getByRole('heading', { level: 1, name: 'Oct 5 – 11' })
    expect(declaredStyle(title).fontSize).toBe('26px')
    expect(screen.getByText('Week 41')).toBeInTheDocument()
    const tag = screen.getByText('Draft')
    expect(declaredStyle(tag).background).toBe('var(--color-accent-100)')
    expect(declaredStyle(tag).color).toBe('var(--color-accent-800)')

    // The assistant: a 32 px grape "Q" avatar beside the plan's ai_note.
    const avatar = screen.getByText('Q')
    expect(declaredStyle(avatar).width).toBe('32px')
    expect(declaredStyle(avatar).background).toBe('var(--color-accent)')
    expect(screen.getByText('I mixed 5 of your regulars with 2 from your Quest log. Tap any day to swap it.')).toBeInTheDocument()

    // Seven day tiles, Monday to Sunday, then the "+ Rebuild week" tile.
    const grid = screen.getByRole('list', { name: 'Dinners this week' })
    expect(declaredStyle(grid).gridTemplateColumns).toBe('repeat(2, minmax(0, 1fr))')
    const items = within(grid).getAllByRole('listitem')
    expect(items).toHaveLength(8)
    expect(items.slice(0, 7).map((item) => item.querySelector('button')!.firstChild!.textContent)).toEqual([
      'MON',
      'TUE',
      'WED · NEW',
      'THU',
      'FRI',
      'SAT · NEW',
      'SUN',
    ])
    expect(within(items[7]!).getByRole('button', { name: 'Rebuild week' })).toBeInTheDocument()
    const monday = tile('MON')
    expect(declaredStyle(monday).minHeight).toBe('86px')
    expect(declaredStyle(monday).borderRadius).toBe('22px')
    expect(declaredStyle(monday).background).toBe('var(--color-surface)')
    expect(declaredStyle(within(monday).getByText('Beef tacos')).fontFamily).toBe('var(--font-heading)')
    expect(declaredStyle(within(monday).getByText('Beef tacos')).fontSize).toBe('15px')
  })

  it('highlights Quest-log picks in teal with the NEW kicker', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')) })
    await renderPage()

    await screen.findByText('WED · NEW')
    const wednesday = tile('WED · NEW')
    expect(declaredStyle(wednesday).background).toBe('var(--color-accent-2-300)')
    expect(declaredStyle(within(wednesday).getByText('WED · NEW')).color).toBe('var(--color-accent-2-900)')
    expect(declaredStyle(tile('SAT · NEW')).background).toBe('var(--color-accent-2-300)')
    // A Regular stays on the surface, with no NEW.
    expect(declaredStyle(tile('THU')).background).toBe('var(--color-surface)')
    expect(screen.queryByText('THU · NEW')).not.toBeInTheDocument()
  })

  it('shows a crock-pot dinner its start-by time in the household zone, and no other dinner one', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')) })
    await renderPage()

    expect(await screen.findByText('Start by 1:30 pm')).toBeInTheDocument()
    expect(within(tile('WED · NEW')).getByText('Start by 1:30 pm')).toBeInTheDocument()
    expect(screen.getAllByText(/^Start by/)).toHaveLength(1)
  })

  it.each([
    ['synced', 'Synced', 'var(--color-accent-2-200)'],
    ['changed', 'Changes not pushed', 'var(--color-accent-100)'],
  ])('tags a %s plan "%s"', async (status, label, background) => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody(status)) })
    await renderPage()

    const tag = await screen.findByText(label)
    expect(declaredStyle(tag).background).toBe(background)
    expect(screen.queryByText('Draft')).not.toBeInTheDocument()
  })

  it('offers Push to Calendar unavailable, with a tooltip saying why', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')) })
    await renderPage()

    const push = await screen.findByRole('button', { name: 'Looks good — push to Calendar' })
    expect(push).toHaveAttribute('aria-disabled', 'true')
    expect(push).toHaveAccessibleDescription('Push to Calendar arrives once Google Calendar can be connected.')
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Push to Calendar arrives once Google Calendar can be connected.')
    expect(declaredStyle(push).minHeight).toBe('50px')
    fireEvent.click(push)
    expect(posts(fetchMock)).toEqual([])
  })

  it('rebuilds a draft at once, showing skeleton tiles until the new plan is read', async () => {
    let plan = planBody('draft')
    let job = draftJob('running')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Rebuild week' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {}`]))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toHaveAttribute('aria-busy', 'true')
    expect(screen.getAllByTestId('skeleton-tile')).toHaveLength(7)
    expect(screen.queryByText('Beef tacos')).not.toBeInTheDocument()

    plan = planBody('draft', ['Lasagna', ...NAMES.slice(1)])
    job = draftJob('succeeded')
    expect(await screen.findByText('Lasagna', {}, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()
  })

  it.each(['synced', 'changed'])('asks before rebuilding a %s plan, and sends confirm', async (status) => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody(status)),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, draftJob('running')),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Rebuild week' }))
    const dialog = await screen.findByRole('dialog', { name: 'Rebuild this week?' })
    expect(dialog).toHaveTextContent('already pushed to Calendar')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep this plan' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posts(fetchMock)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Rebuild week' }))
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {"confirm":true}`]))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
  })

  it('offers "Draft this week" with no plan, and shows skeleton tiles while it drafts', async () => {
    let plan: unknown = null
    let job = draftJob('queued')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => (plan === null ? errorResponse(404, 'not_found') : jsonResponse(200, plan)),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
    })
    await renderPage()

    expect(await screen.findByRole('heading', { name: 'Nothing planned yet' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
    expect(screen.queryByText('Draft')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Looks good — push to Calendar' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Draft this week' }))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-tile')).toHaveLength(7)
    expect(screen.getByText('Drafting your week…')).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${DRAFT} {}`])

    plan = planBody('draft')
    job = draftJob('succeeded')
    expect(await screen.findByText('Beef tacos', {}, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.getByText('Draft')).toBeInTheDocument()
  })

  it('shows a failed draft with Retry and makes up no plan', async () => {
    let job = draftJob('failed', 'ai_failed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Draft this week' }))
    const alert = await screen.findByRole('alert', {}, { timeout: 3000 })
    expect(alert).toHaveTextContent("Couldn't draft this week. The AI assistant couldn't draft it.")
    expect(screen.getByRole('heading', { name: 'Nothing planned yet' })).toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()

    job = draftJob('running')
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {}`, `${DRAFT} {}`]))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it.each([
    ['the week has no plan', null, 'The week has no plan, not draft plan-1.'],
    ['the week has another plan', { ...planBody('draft'), id: 'plan-9' }, "The week's plan is not draft plan-1."],
  ])('shows a succeeded draft with Retry when %s once read, and Retry reads the plan again', async (_, landed, message) => {
    let plan: unknown = null
    let job = draftJob('running')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => (plan === null ? errorResponse(404, 'not_found') : jsonResponse(200, plan)),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Draft this week' }))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
    plan = landed
    job = draftJob('succeeded')
    const alert = await screen.findByRole('alert', {}, { timeout: 3000 })
    expect(alert).toHaveTextContent(`Couldn't read the draft. ${message}`)
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()

    // Retry reads the plan again rather than drafting again: the read is
    // still not the draft.
    let reads = planReads(fetchMock)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(planReads(fetchMock)).toBe(reads + 1))
    expect(await screen.findByRole('alert')).toHaveTextContent(`Couldn't read the draft. ${message}`)
    expect(posts(fetchMock)).toEqual([`${DRAFT} {}`])

    // The next read is the draft, from the server: it lands.
    plan = planBody('draft')
    reads = planReads(fetchMock)
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    expect(planReads(fetchMock)).toBe(reads + 1)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${DRAFT} {}`])
  })

  it("doesn't land a succeeded draft on the plan cached before it, and lands it once the server is read", async () => {
    let plan = planBody('draft')
    let cached = false
    let job = draftJob('running')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => {
        const response = jsonResponse(200, plan)
        if (cached) response.headers.set(cacheFallbackHeader, '1')
        return response
      },
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Rebuild week' }))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
    // The draft succeeds, but the server can't be reached: the service
    // worker answers with the plan cached before the rebuild, same id.
    cached = true
    job = draftJob('succeeded')
    const alert = await screen.findByRole('alert', {}, { timeout: 3000 })
    expect(alert).toHaveTextContent("Couldn't read the draft. The plan came from this device's cache, not the server")
    expect(screen.queryByText('Beef tacos')).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()

    // Retry reads the plan again and drafts nothing: still the cache, so the
    // draft stays unread.
    const reads = planReads(fetchMock)
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(planReads(fetchMock)).toBe(reads + 1))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't read the draft. The plan came from this device's cache, not the server")
    expect(screen.queryByText('Beef tacos')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${DRAFT} {}`])

    // The server is back: the next read of the plan is the draft.
    plan = planBody('draft', ['Lasagna', ...NAMES.slice(1)])
    cached = false
    vi.setSystemTime(new Date('2026-10-07T16:01:00Z'))
    act(() => {
      window.dispatchEvent(new Event('visibilitychange'))
    })
    expect(await screen.findByText('Lasagna')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-tile')).not.toBeInTheDocument()
  })

  it('moves between weeks with the previous and next week buttons', async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
    })
    await renderPage()

    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()
    expect(screen.getByText('Week 42')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
  })

  it("opens on next week once this week's plan_draft time has come", async () => {
    vi.setSystemTime(new Date('2026-10-09T13:00:00Z')) // Friday 09:00 in New York
    stubServer({ [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found') })
    await renderPage()

    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
  })

  it("reports a plan that can't be read, with Retry", async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => errorResponse(500, 'internal') })
    await renderPage()

    expect(await screen.findByRole('alert', {}, { timeout: 3000 })).toHaveTextContent("Couldn't load this week's plan. internal message")
  })

  describe('swap sheet', () => {
    const FRIDAY = `${THIS_WEEK}/days/2026-10-09`

    // swappedBody is the plan as the server answers a swap of Friday to
    // lemon orzo, a Quest-log pick.
    function swappedBody() {
      const body = planBody('draft')
      body.days[4] = { ...body.days[4]!, dinner: { ...body.days[4]!.dinner, id: 'q1', name: 'Lemon orzo' }, is_new: true }
      return body
    }

    // puts lists the PUTs sent, as "path body".
    function puts(fetchMock: Mock<typeof fetch>) {
      return fetchMock.mock.calls
        .filter(([, init]) => init?.method === 'PUT')
        .map(([input, init]) => `${String(input)} ${String(init?.body)}`)
    }

    async function openFriday() {
      fireEvent.click(within(await waitFor(() => tile('FRI'))).getByRole('button'))
      return screen.findByRole('dialog', { name: 'Swap Friday' })
    }

    it('opens on a tapped day, puts it in the swapping state, and closes again', async () => {
      stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
      await renderPage()

      const sheet = await openFriday()
      expect(within(sheet).getByText('Replaces Pizza night')).toBeInTheDocument()

      // The tile: page background, a 2 px grape inset ring, the SWAPPING…
      // kicker and its name muted.
      const friday = tile('FRI · SWAPPING…')
      expect(declaredStyle(friday).background).toBe('var(--color-bg)')
      expect(declaredStyle(friday).boxShadow).toBe('inset 0 0 0 2px var(--color-accent)')
      expect(declaredStyle(within(friday).getByText('FRI · SWAPPING…')).color).toBe('var(--color-accent-700)')
      expect(declaredStyle(within(friday).getByText('Pizza night')).color).toBe('var(--color-neutral-600)')

      // "Quest log · N | Regulars · N", on the Quest log first.
      expect(await within(sheet).findByRole('radio', { name: 'Quest log · 1', checked: true })).toBeInTheDocument()
      expect(await within(sheet).findByRole('radio', { name: 'Regulars · 2' })).toBeInTheDocument()
      const row = within(within(sheet).getByRole('list', { name: 'Quest log' })).getByRole('button', { name: /Lemon orzo/ })
      expect(row).toHaveTextContent('Easy · 25 min')
      const photo = within(sheet).getByRole('img', { hidden: true, name: 'Lemon orzo' })
      expect(declaredStyle(photo).width).toBe('48px')

      fireEvent.keyDown(sheet, { key: 'Escape' })
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(tile('FRI')).toBeInTheDocument()
      expect(screen.queryByText('FRI · SWAPPING…')).not.toBeInTheDocument()
    })

    it('swaps the day at once, closing the sheet, and lands the plan the server answers', async () => {
      let answer!: (response: Response) => void
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        ...lists,
        [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (answer = resolve)),
      })
      await renderPage()

      const sheet = await openFriday()
      fireEvent.click(await within(sheet).findByRole('button', { name: /Lemon orzo/ }))

      // Optimistic: the sheet is closed and Friday is the Quest-log pick, NEW.
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      await screen.findByText('FRI · NEW')
      expect(within(tile('FRI · NEW')).getByText('Lemon orzo')).toBeInTheDocument()
      expect(declaredStyle(tile('FRI · NEW')).background).toBe('var(--color-accent-2-300)')
      await waitFor(() => expect(puts(fetchMock)).toEqual([`${FRIDAY} {"dinner_id":"q1"}`]))

      await act(async () => answer(jsonResponse(200, swappedBody())))
      expect(within(tile('FRI · NEW')).getByText('Lemon orzo')).toBeInTheDocument()
      expect(screen.queryByText('Pizza night')).not.toBeInTheDocument()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()

      // Once the swap has settled the sheet opens again, and reads the Quest
      // log again: the server leaves a dinner on the plan out of it.
      await waitFor(() => {
        fireEvent.click(within(tile('FRI · NEW')).getByRole('button'))
        expect(screen.getByRole('dialog', { name: 'Swap Friday' })).toHaveTextContent('Replaces Lemon orzo')
      })
      await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input) === QUEST_LOG)).toHaveLength(2))
    })

    it('takes the NEW highlight off a day swapped to a Regular', async () => {
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        ...lists,
        [`PUT ${THIS_WEEK}/days/2026-10-07`]: () => {
          const body = planBody('draft')
          body.days[2] = { ...body.days[2]!, dinner: { ...body.days[2]!.dinner, id: 'r1', name: 'Fish tacos' }, is_new: false, start_by: null }
          return jsonResponse(200, body)
        },
      })
      await renderPage()

      fireEvent.click(within(await waitFor(() => tile('WED · NEW'))).getByRole('button'))
      const sheet = await screen.findByRole('dialog', { name: 'Swap Wednesday' })
      fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
      fireEvent.click(within(sheet).getByRole('button', { name: /Fish tacos/ }))

      await screen.findByText('Fish tacos')
      expect(within(tile('WED')).getByText('Fish tacos')).toBeInTheDocument()
      expect(declaredStyle(tile('WED')).background).toBe('var(--color-surface)')
      await waitFor(() => expect(puts(fetchMock)).toEqual([`${THIS_WEEK}/days/2026-10-07 {"dinner_id":"r1"}`]))
      expect(screen.queryByText('Start by 1:30 pm')).not.toBeInTheDocument()
    })

    it('puts the day back and shows the failure, with Retry, when the swap is refused', async () => {
      let refuse = true
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        ...lists,
        [`PUT ${FRIDAY}`]: () => (refuse ? errorResponse(409, 'dinner_not_plannable') : jsonResponse(200, swappedBody())),
      })
      await renderPage()

      const sheet = await openFriday()
      fireEvent.click(await within(sheet).findByRole('button', { name: /Lemon orzo/ }))

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent("Couldn't swap Friday's dinner. dinner_not_plannable message")
      expect(within(tile('FRI')).getByText('Pizza night')).toBeInTheDocument()
      expect(screen.queryByText('Lemon orzo')).not.toBeInTheDocument()

      refuse = false
      fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
      expect(await screen.findByText('Lemon orzo')).toBeInTheDocument()
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
      expect(puts(fetchMock)).toHaveLength(2)
    })

    it('marks dinners already this week, which cannot be chosen', async () => {
      const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
      await renderPage()

      const sheet = await openFriday()
      fireEvent.click(await within(sheet).findByRole('radio', { name: 'Regulars · 2' }))
      const regulars = within(sheet).getByRole('list', { name: 'Regulars' })
      const [tacos, fish] = within(regulars).getAllByRole('listitem')
      expect(within(tacos!).getByText('Already this week')).toBeInTheDocument()
      expect(within(tacos!).queryByRole('button')).not.toBeInTheDocument()
      expect(within(fish!).queryByText('Already this week')).not.toBeInTheDocument()
      expect(within(fish!).getByRole('button', { name: /Fish tacos/ })).toBeInTheDocument()

      fireEvent.click(within(tacos!).getByText('Beef tacos'))
      expect(screen.getByRole('dialog', { name: 'Swap Friday' })).toBeInTheDocument()
      expect(puts(fetchMock)).toEqual([])
    })

    // leaveAndReturn goes to next week and back, remounting this week's board.
    async function leaveAndReturn() {
      fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
      expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
      await screen.findByText('FRI · NEW')
    }

    it('sends one swap at a time even after leaving the week and coming back', async () => {
      let answer!: (response: Response) => void
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
        ...lists,
        [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (answer = resolve)),
      })
      await renderPage()

      fireEvent.click(await within(await openFriday()).findByRole('button', { name: /Lemon orzo/ }))
      await waitFor(() => expect(puts(fetchMock)).toHaveLength(1))
      await leaveAndReturn()

      // Friday's swap is still pending: Monday does not open.
      fireEvent.click(within(tile('MON')).getByRole('button'))
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeDisabled()

      await act(async () => answer(jsonResponse(200, swappedBody())))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeEnabled())
      fireEvent.click(within(tile('MON')).getByRole('button'))
      expect(screen.getByRole('dialog', { name: 'Swap Monday' })).toBeInTheDocument()
      expect(puts(fetchMock)).toHaveLength(1)
    })

    it('shows the failure of a swap that ended while the week was left', async () => {
      let refuse!: () => void
      stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
        ...lists,
        [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (refuse = () => resolve(errorResponse(409, 'dinner_not_plannable')))),
      })
      await renderPage()

      fireEvent.click(await within(await openFriday()).findByRole('button', { name: /Lemon orzo/ }))
      fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
      expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
      await act(async () => refuse())
      fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))

      expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't swap Friday's dinner. dinner_not_plannable message")
      expect(within(tile('FRI')).getByText('Pizza night')).toBeInTheDocument()
    })

    it('waits for a pending swap before Rebuild week, keeping its failure', async () => {
      let refuse!: () => void
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        ...lists,
        [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (refuse = () => resolve(errorResponse(409, 'dinner_not_plannable')))),
        [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
        [`GET ${JOB}`]: () => jsonResponse(200, draftJob('running')),
      })
      await renderPage()

      fireEvent.click(await within(await openFriday()).findByRole('button', { name: /Lemon orzo/ }))
      await screen.findByText('FRI · NEW')
      const rebuild = screen.getByRole('button', { name: 'Rebuild week' })
      expect(rebuild).toBeDisabled()
      fireEvent.click(rebuild)
      expect(posts(fetchMock)).toEqual([])

      await act(async () => refuse())
      expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't swap Friday's dinner. dinner_not_plannable message")
      expect(within(tile('FRI')).getByText('Pizza night')).toBeInTheDocument()

      // Once it has failed, Rebuild week drafts, leaving the failure behind.
      fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
      await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {}`]))
      expect(await screen.findByText('Drafting your week…')).toBeInTheDocument()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    })
  })
})

describe.each([
  { width: 1180, layout: 'tablet', photoHeight: '96px', nameSize: '16px' },
  { width: 1440, layout: 'desktop', photoHeight: '110px', nameSize: '17px' },
])('PlanPage at $width px (mocks 2c, 2d)', ({ width, layout, photoHeight, nameSize }) => {
  beforeEach(() => mockViewport(width))

  // column is the grid's column of a day, by its name: "Monday".
  function column(day: string) {
    return within(screen.getByRole('list', { name: 'Dinners this week' })).getByRole('listitem', { name: day })
  }
  // card is the card in a day's column, holding its dinner.
  function card(day: string, dinnerName: string) {
    return within(column(day)).getByText(dinnerName).parentElement!
  }

  it('renders the plan manager: header, actions, sync status and the 7-column grid', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
    await renderPage()

    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    expect(screen.getByText('Week 41 · Plan')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Previous week' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next week' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'This week' }) !== null).toBe(layout === 'desktop')

    // Actions: the shopping list and Push to Calendar unavailable, saying
    // why; Rebuild week outlined and working.
    const shopping = screen.getByRole('button', { name: 'Shopping list' })
    expect(shopping).toHaveAttribute('aria-disabled', 'true')
    expect(shopping).toHaveAccessibleDescription('The shopping list arrives once the Supply run is built.')
    const push = screen.getByRole('button', { name: 'Push to Calendar' })
    expect(push).toHaveAttribute('aria-disabled', 'true')
    expect(push).toHaveAccessibleDescription('Push to Calendar arrives once Google Calendar can be connected.')
    expect(declaredStyle(push).background).toBe('var(--color-accent)')
    const rebuild = screen.getByRole('button', { name: 'Rebuild week' })
    expect(rebuild).toBeEnabled()
    expect(declaredStyle(rebuild).borderColor).toBe('var(--color-neutral-300)')
    fireEvent.click(shopping)
    fireEvent.click(push)
    expect(posts(fetchMock)).toEqual([])
    // Mobile's own pieces are not shown.
    expect(screen.queryByRole('button', { name: 'Looks good — push to Calendar' })).not.toBeInTheDocument()
    expect(screen.queryByText('Draft')).not.toBeInTheDocument()

    expect(screen.getByText('Not pushed to Google Calendar yet')).toBeInTheDocument()

    // Seven columns, Monday to Sunday, 12 px apart.
    const grid = screen.getByRole('list', { name: 'Dinners this week' })
    expect(declaredStyle(grid).gridTemplateColumns).toBe('repeat(7, minmax(0, 1fr))')
    expect(declaredStyle(grid).gap).toBe('12px')
    expect(within(grid).getAllByRole('listitem').map((li) => li.getAttribute('aria-label'))).toEqual([
      'Monday',
      'Tuesday',
      'Wednesday',
      'Thursday',
      'Friday',
      'Saturday',
      'Sunday',
    ])

    // Monday: day label, the washed photo, the name in Bagel, minutes and
    // the Regular tag, on the surface.
    expect(column('Monday')).toHaveTextContent(/^MON5/)
    const monday = card('Monday', 'Beef tacos')
    expect(declaredStyle(monday).background).toBe('var(--color-surface)')
    const photo = within(monday).getByRole('img', { hidden: true, name: 'Beef tacos' })
    expect(photo).toHaveAttribute('src', '/media/photos/tacos-400.jpg')
    expect(declaredStyle(photo).height).toBe(photoHeight)
    const name = within(monday).getByText('Beef tacos')
    expect(declaredStyle(name).fontFamily).toBe('var(--font-heading)')
    expect(declaredStyle(name).fontSize).toBe(nameSize)
    expect(within(monday).getByText('30 min')).toBeInTheDocument()
    expect(declaredStyle(within(monday).getByText('Regular')).background).toBe('var(--color-neutral-100)')
    expect(within(monday).queryByText(/Start by/)).not.toBeInTheDocument()

    // Wednesday: a crock-pot Side quest on teal-200, with its start-by time.
    const wednesday = card('Wednesday', 'Crock pot tikka masala')
    expect(declaredStyle(wednesday).background).toBe('var(--color-accent-2-200)')
    expect(within(wednesday).getByText('240 min')).toBeInTheDocument()
    expect(within(wednesday).getByText('Start by 1:30 pm')).toBeInTheDocument()
    expect(within(wednesday).getByText('Side quest')).toBeInTheDocument()
    expect(within(column('Wednesday')).getByRole('button', { name: 'Swap Wednesday' })).toBeInTheDocument()
  })

  it('says a synced plan is on Google Calendar', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('synced')), ...lists })
    await renderPage()

    const status = await screen.findByText('Synced to Google Calendar')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
  })

  if (layout === 'desktop') {
    it('shows the Quest log and Regulars in the 290 px right panel', async () => {
      stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
      await renderPage()

      const panel = screen.getByRole('complementary', { name: 'Quest log and Regulars' })
      expect(declaredStyle(panel).borderLeft).toBe('1px solid var(--color-divider)')
      expect(declaredStyle(panel.parentElement!).gridTemplateColumns).toBe('minmax(0, 1fr) 290px')
      expect(await within(panel).findByRole('radio', { name: 'Quest log · 1', checked: true })).toBeInTheDocument()
      expect(await within(panel).findByRole('radio', { name: 'Regulars · 2' })).toBeInTheDocument()
      expect(within(panel).getByText('Drag a dinner onto a day to swap it in.')).toBeInTheDocument()

      const orzo = within(within(panel).getByRole('list', { name: 'Quest log' })).getByRole('listitem')
      expect(within(orzo).getByText('Lemon orzo').nextElementSibling).toHaveTextContent('Easy')
      expect(declaredStyle(within(orzo).getByRole('img', { hidden: true, name: 'Lemon orzo' })).width).toBe('48px')
      // The 6-dot grip.
      expect(orzo.lastElementChild!.children).toHaveLength(6)

      fireEvent.click(within(panel).getByRole('radio', { name: 'Regulars · 2' }))
      const regulars = within(within(panel).getByRole('list', { name: 'Regulars' })).getAllByRole('listitem')
      expect(regulars).toHaveLength(2)
      expect(within(regulars[0]!).getByText('Beef tacos').nextElementSibling).toHaveTextContent('Easy')
      expect(within(regulars[1]!).getByText('Fish tacos').nextElementSibling).toHaveTextContent('Medium')

      expect(within(panel).getByRole('link', { name: 'Swipe more side quests' })).toHaveAttribute('href', '/discover')
      expect(screen.queryByRole('region', { name: 'Quest log' })).not.toBeInTheDocument()
    })
  } else {
    it('shows the Quest log as a strip of chips under the grid', async () => {
      stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
      await renderPage()

      const strip = screen.getByRole('region', { name: 'Quest log' })
      expect(await within(strip).findByRole('heading', { name: 'Quest log · 1' })).toBeInTheDocument()
      expect(within(strip).getByText('Drag a dinner onto a day to swap it in.')).toBeInTheDocument()
      const chip = within(within(strip).getByRole('list', { name: 'Quest log dinners' })).getByRole('listitem')
      expect(chip).toHaveTextContent('Lemon orzo')
      expect(declaredStyle(within(chip).getByRole('img', { hidden: true, name: 'Lemon orzo' })).width).toBe('28px')
      // The strip follows the grid.
      const grid = await screen.findByRole('list', { name: 'Dinners this week' })
      expect(grid.compareDocumentPosition(strip) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
    })
  }

  it('moves between weeks with the previous and next week buttons', async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`GET ${PREVIOUS_WEEK}`]: () => errorResponse(404, 'not_found'),
      ...lists,
    })
    await renderPage()

    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()
    expect(screen.getByText('Week 42 · Plan')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Sep 28 – Oct 4' })).toBeInTheDocument()
    expect(screen.getByText('Week 40 · Plan')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
  })

  if (layout === 'desktop') {
    it('comes back to this week with the "This week" pill', async () => {
      stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
        ...lists,
      })
      await renderPage()

      expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
      const thisWeek = screen.getByRole('button', { name: 'This week' })
      expect(declaredStyle(thisWeek).background).toBe('var(--color-surface)')
      fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
      expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'This week' }))
      expect(await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
      expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    })

    it("comes back to this week even when the plan opened on next week's", async () => {
      vi.setSystemTime(new Date('2026-10-09T13:00:00Z')) // Friday 09:00 in New York
      stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
        ...lists,
      })
      await renderPage()

      expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'This week' }))
      expect(await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })).toBeInTheDocument()
    })
  }

  it("opens the swap sheet from a day's swap icon and swaps the dinner in", async () => {
    const FRIDAY = `${THIS_WEEK}/days/2026-10-09`
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      ...lists,
      [`PUT ${FRIDAY}`]: () => new Promise<Response>(() => undefined),
    })
    await renderPage()

    fireEvent.click(within(await waitFor(() => column('Friday'))).getByRole('button', { name: 'Swap Friday' }))
    const sheet = await screen.findByRole('dialog', { name: 'Swap Friday' })
    expect(within(sheet).getByText('Replaces Pizza night')).toBeInTheDocument()
    // The card being swapped is ringed in grape.
    expect(declaredStyle(card('Friday', 'Pizza night')).boxShadow).toBe('inset 0 0 0 2px var(--color-accent)')

    fireEvent.keyDown(sheet, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(declaredStyle(card('Friday', 'Pizza night')).boxShadow).toBe('')

    fireEvent.click(within(column('Friday')).getByRole('button', { name: 'Swap Friday' }))
    const again = await screen.findByRole('dialog', { name: 'Swap Friday' })
    fireEvent.click(await within(again).findByRole('button', { name: /Lemon orzo/ }))

    // Optimistic: the sheet is closed and Friday is the Side quest, on teal.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    const friday = await waitFor(() => card('Friday', 'Lemon orzo'))
    expect(within(friday).getByText('Side quest')).toBeInTheDocument()
    expect(within(friday).getByText('25 min')).toBeInTheDocument()
    expect(declaredStyle(friday).background).toBe('var(--color-accent-2-200)')
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1))
    // Rebuild week waits for the swap.
    expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeDisabled()
  })

  it.each(['synced', 'changed'])('asks before rebuilding a %s plan, and sends confirm', async (status) => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody(status)),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, draftJob('running')),
      ...lists,
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
    const dialog = await screen.findByRole('dialog', { name: 'Rebuild this week?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep this plan' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posts(fetchMock)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Rebuild week' }))
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {"confirm":true}`]))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()
    expect(screen.getAllByTestId('skeleton-tile')).toHaveLength(7)
    expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeDisabled()
  })

  it('rebuilds a draft at once', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, draftJob('running')),
      ...lists,
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${DRAFT} {}`]))
  })
})
