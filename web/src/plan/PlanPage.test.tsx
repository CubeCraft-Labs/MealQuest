import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { cacheFallbackHeader } from '../sw/routes.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { ago, cooking, duration, PlanPage } from './PlanPage.tsx'

const THIS_WEEK = '/api/v1/plans/2026-10-05'
const NEXT_WEEK = '/api/v1/plans/2026-10-12'
const PREVIOUS_WEEK = '/api/v1/plans/2026-09-28'
const DRAFT = `${THIS_WEEK}/draft`
const PUSH = `${THIS_WEEK}/push-calendar`
const JOB = '/api/v1/jobs/j1'

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const NAMES = ['Beef tacos', 'Spaghetti bolognese', 'Crock pot tikka masala', 'Chicken stir-fry', 'Pizza night', 'Korean beef bowls', 'Sheet-pan salmon']

// planBody is the plan of the week of Oct 5 as GET /api/v1/plans/{weekStart}
// sends it: Wednesday's crock-pot tikka masala and Saturday's bowls are
// Quest-log picks, and the tikka masala must start by 1:30 pm New York time
// to be ready for 5:30 pm dinner. Monday's tacos have a stored photo. A plan
// pushed to Calendar was pushed two minutes before the tests' noon.
function planBody(status: string, names = NAMES) {
  return {
    id: 'plan-1',
    week_start: '2026-10-05',
    week_label: 'Week 41 · Oct 5 – 11',
    status,
    ai_note: 'I mixed 5 of your regulars with 2 from your Quest log. Tap any day to swap it.',
    calendar_synced_at: status === 'draft' ? null : '2026-10-07T15:58:00Z',
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

// syncedNow is the plan of the week of Oct 5 as a push answers it: synced
// at the tests' noon, with what became of the week's Supply run, made
// unless told otherwise.
function syncedNow(supplyRun: { status: string; error: { code: string; message: string; retryable: boolean } | null } = GENERATED) {
  return { ...planBody('synced'), calendar_synced_at: '2026-10-07T16:00:00Z', supply_run: supplyRun }
}

// The Supply run outcomes a push answers with, as the server sends them.
const GENERATED = { status: 'generated', error: null }
const ARCHIVED = {
  status: 'archived',
  error: { code: 'archived', message: 'the Supply run is in the Supply archive', retryable: false },
}
const PLAN_CHANGED = {
  status: 'failed',
  error: {
    code: 'plan_changed',
    message: 'the plan is on Calendar as pushed, but it was edited before its Supply run was made, so no Supply run was made from it; push again',
    retryable: true,
  },
}
const SUPPLY_RUN_FAILED = {
  status: 'failed',
  error: {
    code: 'supply_run_failed',
    message: 'the plan is on Calendar, but its Supply run could not be made; make it again from the Supply run',
    retryable: true,
  },
}

// deferred is a response the test sends when it chooses.
function deferred() {
  let send!: (response: Response) => void
  const response = new Promise<Response>((resolve) => {
    send = resolve
  })
  return { response, send }
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

// SUPPLY_RUN is the Supply run of a week, as GET /api/v1/shopping/{weekStart}.
const SUPPLY_RUN = /^GET \/api\/v1\/shopping\/\d{4}-\d{2}-\d{2}$/

// stubServer answers "METHOD path" requests from responders, with /me and
// the household settings, and no Supply run (404) for a week given none;
// any other request fails the test.
function stubServer(responders: Record<string, Responder>) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : SUPPLY_RUN.test(key) ? () => errorResponse(404, 'not_found') : undefined
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
          <Routes>
            <Route path="/plan" element={<PlanPage />} />
            <Route path="/plan/shopping" element={<Opened />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

// Opened stands in for the screen a link opened, saying where it is.
function Opened() {
  const { pathname, search } = useLocation()
  return <p>Opened {pathname + search}</p>
}

// supplyRun is the Supply run of the week of Oct 5 with needed of its 32
// items still to buy.
function supplyRun(needed: number) {
  return {
    weekStart: '2026-10-05',
    label: 'Week 41 · Oct 5 – 11',
    title: null,
    status: 'current',
    sent_to_chat_at: null,
    needed_count: needed,
    total_count: 32,
    groups: [],
  }
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

  it('pushes the plan to Calendar, showing progress, then the plan as synced', async () => {
    const answer = deferred()
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => answer.response,
    })
    await renderPage()

    expect(await screen.findByText('Not pushed to Google Calendar yet')).toBeInTheDocument()
    const push = screen.getByRole('button', { name: 'Looks good — push to Calendar' })
    expect(declaredStyle(push).minHeight).toBe('50px')
    fireEvent.click(push)
    await waitFor(() => expect(posts(fetchMock)).toEqual([`${PUSH} undefined`]))
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(new Headers(call[1]?.headers).get('X-MealQuest-CSRF')).toBe(ME.csrfToken)

    // In progress: the button says so and is disabled, as is Rebuild week,
    // and no day can be swapped.
    const pushing = await screen.findByRole('button', { name: 'Pushing to Calendar…' })
    expect(pushing).toBeDisabled()
    expect(pushing).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeDisabled()
    fireEvent.click(within(tile('MON')).getByRole('button'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    answer.send(jsonResponse(200, syncedNow()))
    const status = await screen.findByText('Synced to Google Calendar just now')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
    expect(status.querySelector('svg')).not.toBeNull()
    expect(screen.getByText('Synced')).toBeInTheDocument()
    expect(screen.queryByText('Draft')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeEnabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    // The plan shown is the one the push answered with: no second read.
    expect(planReads(fetchMock)).toBe(1)
  })

  it('says a synced plan is on Google Calendar, and how long ago it was pushed', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('synced')) })
    await renderPage()

    const status = await screen.findByText('Synced to Google Calendar 2 min ago')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
    expect(status.querySelector('svg')).not.toBeNull()
  })

  it('says changes to a pushed plan are not pushed, and pushes them', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('changed')),
      [`POST ${PUSH}`]: () => jsonResponse(200, syncedNow()),
    })
    await renderPage()

    expect(await screen.findByText('Changes not pushed')).toBeInTheDocument()
    const status = screen.getByText('Changes not pushed to Google Calendar')
    expect(declaredStyle(status).color).toBe('var(--color-neutral-700)')
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(screen.queryByText('Changes not pushed')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])
  })

  it.each([
    ['google_reconnect_required', 'Google access was revoked or expired.', 'Reconnect Google Calendar'],
    ['no_calendar', 'No calendar is chosen for dinners yet.', 'Choose a calendar in Settings'],
    ['not_connected', 'Google Calendar is not connected.', 'Connect Google Calendar'],
    ['calendar_unavailable', 'The chosen calendar was deleted or MealQuest can no longer add dinners to it.', 'Choose another calendar'],
  ])('sends a %s push failure to the Google Calendar settings', async (code, reason, fix) => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => errorResponse(409, code),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(`Couldn't push to Calendar. ${reason} ${fix}`)
    expect(within(alert).getByRole('link', { name: fix })).toHaveAttribute('href', '/settings/calendar')
    expect(within(alert).queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
    // The plan is as it was: not synced.
    expect(screen.getByText('Draft')).toBeInTheDocument()
    expect(screen.getByText('Not pushed to Google Calendar yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeEnabled()
  })

  it('marks the Google Calendar connection to be read again when Google refused the grant', async () => {
    const connection = { status: 'connected', calendar_id: 'c1', calendar_name: 'Dinners', connected_at: null, last_sync_error: null }
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    const queryClient = createQueryClient()
    queryClient.setQueryData(['integrations', 'google'], connection)
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => errorResponse(409, 'google_reconnect_required'),
    })
    await api.loadMe()
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={['/plan']}>
            <PlanPage />
          </MemoryRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByRole('link', { name: 'Reconnect Google Calendar' })).toBeInTheDocument()
    // The settings screen reads it again as it opens: it is now reconnect
    // required.
    expect(queryClient.getQueryState(['integrations', 'google'])?.isInvalidated).toBe(true)
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])
  })

  it('says why any other push failed, and pushes again on Retry', async () => {
    let push = () => errorResponse(502, 'google_failed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't push to Calendar. google_failed message")
    expect(within(alert).queryByRole('link')).not.toBeInTheDocument()

    push = () => jsonResponse(200, syncedNow())
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
  })

  it('reads the plan again when it changed while being pushed', async () => {
    let plan = planBody('draft')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`POST ${PUSH}`]: () => errorResponse(409, 'plan_changed'),
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    plan = planBody('draft', ['Lasagna', ...NAMES.slice(1)])
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      "Couldn't push to Calendar. The plan changed while it was being pushed; push it again.",
    )
    expect(await screen.findByText('Lasagna')).toBeInTheDocument()
    expect(planReads(fetchMock)).toBe(2)
  })

  it("reads the week's Supply run again once a push has made it, and says it was made", async () => {
    let run = () => errorResponse(404, 'not_found')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => {
        run = () => jsonResponse(200, supplyRun(28))
        return jsonResponse(200, syncedNow(GENERATED))
      },
      'GET /api/v1/shopping/2026-10-05': () => run(),
    })
    await renderPage()

    expect(await screen.findByRole('link', { name: 'Shopping list' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent("Made this week's Supply run from the plan.")
    // The list made is read at once, not after the cached 404 goes stale.
    expect(await screen.findByRole('link', { name: 'Shopping list · 28' })).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/shopping/2026-10-05')).toHaveLength(2)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('says a Supply run in the Supply archive was left as it was', async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => jsonResponse(200, syncedNow(ARCHIVED)),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent("This week's Supply run is in the Supply archive, so it was left as it was.")
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('says no Supply run was made when the plan changed after it was pushed, reads the plan again, and pushes again on Retry', async () => {
    let plan = planBody('draft')
    let push = () => {
      plan = planBody('changed', ['Lasagna', ...NAMES.slice(1)])
      return jsonResponse(200, syncedNow(PLAN_CHANGED))
    }
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      'Pushed to Calendar, but the plan changed before its Supply run was made, so no shopping list was made from it; push it again.',
    )
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    // The plan edited meanwhile is the one shown, not the one pushed.
    expect(await screen.findByText('Lasagna')).toBeInTheDocument()
    expect(screen.getByText('Changes not pushed to Google Calendar')).toBeInTheDocument()
    expect(planReads(fetchMock)).toBe(2)

    push = () => jsonResponse(200, syncedNow(GENERATED))
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('status')).toHaveTextContent("Made this week's Supply run from the plan.")
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
  })

  it('says the Supply run could not be made after the push, and makes the shopping list again', async () => {
    let generate = () => errorResponse(500, 'internal')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`POST ${PUSH}`]: () => jsonResponse(200, syncedNow(SUPPLY_RUN_FAILED)),
      'POST /api/v1/shopping/2026-10-05/generate': () => generate(),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Looks good — push to Calendar' }))
    // Calendar has the plan, but the push is not reported a quiet success.
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Pushed to Calendar, but this week's Supply run couldn't be made.")
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

    fireEvent.click(within(alert).getByRole('button', { name: 'Make the shopping list' }))
    expect(
      await screen.findByText("Pushed to Calendar, but this week's Supply run couldn't be made. Couldn't make the shopping list. internal message"),
    ).toBeInTheDocument()

    generate = () => jsonResponse(200, { ...supplyRun(28), skipped: [] })
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Make the shopping list' }))
    expect(await screen.findByRole('status')).toHaveTextContent("Made this week's Supply run from the plan.")
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Shopping list · 28' })).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([
      `${PUSH} undefined`,
      '/api/v1/shopping/2026-10-05/generate undefined',
      '/api/v1/shopping/2026-10-05/generate undefined',
    ])
  })

  it('pushes nothing again until the changed plan is read and shown', async () => {
    let read: () => Response | Promise<Response> = () => jsonResponse(200, planBody('draft'))
    let push = () => errorResponse(409, 'plan_changed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => read(),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    const reread = deferred()
    read = () => reread.response
    const pushButton = screen.getByRole('button', { name: 'Looks good — push to Calendar' })
    fireEvent.click(pushButton)
    await waitFor(() => expect(planReads(fetchMock)).toBe(2))
    // While the changed plan is read, the push stays busy: neither Push
    // nor Retry sends it again.
    expect(screen.getByRole('button', { name: 'Pushing to Calendar…' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Pushing to Calendar…' }))
    expect(screen.getByText('Beef tacos')).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])

    reread.send(jsonResponse(200, planBody('draft', ['Lasagna', ...NAMES.slice(1)])))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't push to Calendar. The plan changed while it was being pushed; push it again.")
    expect(screen.getByText('Lasagna')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeEnabled()

    push = () => jsonResponse(200, syncedNow())
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
  })

  it('pushes nothing again until the changed plan is shown, even after leaving the week and coming back', async () => {
    let read: () => Response | Promise<Response> = () => jsonResponse(200, planBody('draft'))
    let push = () => errorResponse(409, 'plan_changed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => read(),
      [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    const reread = deferred()
    read = () => reread.response
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    await waitFor(() => expect(planReads(fetchMock)).toBe(2))

    // Leaving the week and coming back while the changed plan is read
    // remounts the board: the push is still busy, and sends nothing again.
    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
    await screen.findByText('Beef tacos')
    expect(screen.getByRole('button', { name: 'Pushing to Calendar…' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Pushing to Calendar…' }))
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])

    await act(async () => reread.send(jsonResponse(200, planBody('draft', ['Lasagna', ...NAMES.slice(1)]))))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't push to Calendar. The plan changed while it was being pushed; push it again.")
    expect(screen.getByText('Lasagna')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeEnabled()

    push = () => jsonResponse(200, syncedNow())
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
  })

  it("pushes nothing again while the changed plan's read is answered from the cache", async () => {
    let read = () => jsonResponse(200, planBody('draft'))
    let push = () => errorResponse(409, 'plan_changed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => read(),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    // The server can't be reached: the service worker answers the read of
    // the changed plan with the plan it cached before.
    read = () => {
      const response = jsonResponse(200, planBody('draft'))
      response.headers.set(cacheFallbackHeader, '1')
      return response
    }
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    const cachedCopy = "The plan changed while it was being pushed. The plan shown came from this device's cache, not the server; read it again before pushing."
    const failed = await screen.findByRole('alert')
    expect(planReads(fetchMock)).toBe(2)
    expect(screen.getByText('Beef tacos')).toBeInTheDocument()
    const pushButton = screen.getByRole('button', { name: 'Looks good — push to Calendar' })
    expect(pushButton).toBeDisabled()
    fireEvent.click(pushButton)
    expect(failed).toHaveTextContent(cachedCopy)

    // Retry reads the plan again rather than pushing: still the cache, so
    // nothing is pushed.
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(planReads(fetchMock)).toBe(3))
    expect(await screen.findByRole('alert')).toHaveTextContent(cachedCopy)
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeDisabled()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])

    // The server is back: Retry shows the changed plan, and only then may it
    // be pushed.
    read = () => jsonResponse(200, planBody('draft', ['Lasagna', ...NAMES.slice(1)]))
    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Lasagna')).toBeInTheDocument()
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't push to Calendar. The plan changed while it was being pushed; push it again.")
    expect(screen.getByRole('button', { name: 'Looks good — push to Calendar' })).toBeEnabled()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`])

    push = () => jsonResponse(200, syncedNow())
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
  })

  it('shows the changed plan read while the week was left, and pushes it', async () => {
    let read: () => Response | Promise<Response> = () => jsonResponse(200, planBody('draft'))
    let push = () => errorResponse(409, 'plan_changed')
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => read(),
      [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`POST ${PUSH}`]: () => push(),
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    const reread = deferred()
    read = () => reread.response
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    await waitFor(() => expect(planReads(fetchMock)).toBe(2))
    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
    await act(async () => reread.send(jsonResponse(200, planBody('draft', ['Lasagna', ...NAMES.slice(1)]))))
    read = () => jsonResponse(200, planBody('draft', ['Lasagna', ...NAMES.slice(1)]))
    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))

    // The board comes back on the changed plan: Push sends that one.
    expect(await screen.findByText('Lasagna')).toBeInTheDocument()
    expect(screen.queryByText('Beef tacos')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('The plan changed while it was being pushed; push it again.')
    push = () => jsonResponse(200, syncedNow())
    fireEvent.click(screen.getByRole('button', { name: 'Looks good — push to Calendar' }))
    expect(await screen.findByText('Synced to Google Calendar just now')).toBeInTheDocument()
    expect(posts(fetchMock)).toEqual([`${PUSH} undefined`, `${PUSH} undefined`])
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

  it("links to the week's Supply run under the Draft board, saying how many items are still to buy", async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      'GET /api/v1/shopping/2026-10-05': () => jsonResponse(200, supplyRun(19)),
    })
    await renderPage()

    const shopping = await screen.findByRole('link', { name: 'Shopping list · 19' })
    expect(shopping).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    expect(declaredStyle(shopping).color).toBe('var(--color-accent-700)')
    // It follows the push button and the sync status.
    const push = screen.getByRole('button', { name: 'Looks good — push to Calendar' })
    expect(push.compareDocumentPosition(shopping) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    fireEvent.click(shopping)
    expect(await screen.findByText('Opened /plan/shopping?week=2026-10-05')).toBeInTheDocument()
  })

  it("shows a failed read of the week's shopping list with Retry, the link still opening the Supply run", async () => {
    let failing = true
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      'GET /api/v1/shopping/2026-10-05': () => (failing ? errorResponse(500, 'internal') : jsonResponse(200, supplyRun(19))),
    })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load this week's shopping list. internal message")
    expect(screen.getByRole('link', { name: 'Shopping list' })).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    // Retry reads it again.
    failing = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    const shopping = await screen.findByRole('link', { name: 'Shopping list · 19' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([input]) => String(input) === '/api/v1/shopping/2026-10-05')).toHaveLength(2)
    fireEvent.click(shopping)
    expect(await screen.findByText('Opened /plan/shopping?week=2026-10-05')).toBeInTheDocument()
  })

  it("drops the count when reading the week's shopping list again fails, showing the failure with Retry", async () => {
    let failing = false
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      'GET /api/v1/shopping/2026-10-05': () => (failing ? errorResponse(500, 'internal') : jsonResponse(200, supplyRun(19))),
    })
    await renderPage()
    expect(await screen.findByRole('link', { name: 'Shopping list · 19' })).toBeInTheDocument()

    // Coming back to the page once the Supply run is stale reads it again,
    // and that read fails.
    failing = true
    vi.setSystemTime(new Date('2026-10-07T16:01:00Z'))
    act(() => {
      window.dispatchEvent(new Event('visibilitychange'))
    })
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load this week's shopping list. internal message")
    expect(screen.getByRole('link', { name: 'Shopping list' })).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    expect(screen.queryByRole('link', { name: 'Shopping list · 19' })).not.toBeInTheDocument()
    // Retry reads it again, and the count is back.
    failing = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('link', { name: 'Shopping list · 19' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('links to the Supply run with no count while the week has no shopping list', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')) })
    await renderPage()

    expect(await screen.findByText('Beef tacos')).toBeInTheDocument()
    await waitFor(() => expect(fetchMock.mock.calls.map(([input]) => String(input))).toContain('/api/v1/shopping/2026-10-05'))
    const shopping = screen.getByRole('link', { name: 'Shopping list' })
    expect(shopping).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    // No list is not a failure.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    fireEvent.click(shopping)
    expect(await screen.findByText('Opened /plan/shopping?week=2026-10-05')).toBeInTheDocument()
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
  { width: 768, layout: 'tablet', photoHeight: '96px', nameSize: '16px' },
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

    // Actions: the shopping list, with no count while the week has no
    // Supply run, and Rebuild week outlined; Push to Calendar primary.
    const shopping = await screen.findByRole('link', { name: 'Shopping list' })
    expect(shopping).not.toHaveAttribute('aria-disabled')
    expect(shopping).not.toHaveAccessibleDescription()
    const push = screen.getByRole('button', { name: 'Push to Calendar' })
    expect(push).toBeEnabled()
    expect(push).not.toHaveAttribute('aria-disabled')
    expect(declaredStyle(push).background).toBe('var(--color-accent)')
    const rebuild = screen.getByRole('button', { name: 'Rebuild week' })
    expect(rebuild).toBeEnabled()
    expect(declaredStyle(rebuild).borderColor).toBe('var(--color-neutral-300)')
    expect(posts(fetchMock)).toEqual([])
    // Mobile's own pieces are not shown.
    expect(screen.queryByRole('button', { name: 'Looks good — push to Calendar' })).not.toBeInTheDocument()
    expect(screen.queryByText('Draft')).not.toBeInTheDocument()

    expect(screen.getByText('Not pushed to Google Calendar yet')).toBeInTheDocument()

    // Seven columns, Monday to Sunday, 12 px apart.
    const grid = screen.getByRole('list', { name: 'Dinners this week' })
    expect(declaredStyle(grid).gridTemplateColumns).toBe('repeat(7, minmax(112px, 1fr))')
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

    // Monday: day label, the washed photo, the name in Bagel, method and
    // minutes, and the Regular tag, on the surface.
    expect(column('Monday')).toHaveTextContent(/^MON5/)
    const monday = card('Monday', 'Beef tacos')
    expect(declaredStyle(monday).background).toBe('var(--color-surface)')
    const photo = within(monday).getByRole('img', { hidden: true, name: 'Beef tacos' })
    expect(photo).toHaveAttribute('src', '/media/photos/tacos-400.jpg')
    expect(declaredStyle(photo).height).toBe(photoHeight)
    const name = within(monday).getByText('Beef tacos')
    expect(declaredStyle(name).fontFamily).toBe('var(--font-heading)')
    expect(declaredStyle(name).fontSize).toBe(nameSize)
    // Testing Library reads the duration's no-break spaces as spaces.
    expect(within(monday).getByText('Stovetop · 30 min')).toBeInTheDocument()
    expect(declaredStyle(within(monday).getByText('Regular')).background).toBe('var(--color-neutral-100)')
    expect(within(monday).queryByText(/Start by/)).not.toBeInTheDocument()

    // Wednesday: a crock-pot Side quest on teal-200, with its start-by time.
    const wednesday = card('Wednesday', 'Crock pot tikka masala')
    expect(declaredStyle(wednesday).background).toBe('var(--color-accent-2-200)')
    expect(within(wednesday).getByText('Crock pot · 4 hr')).toBeInTheDocument()
    expect(within(wednesday).getByText('Start by 1:30 pm')).toBeInTheDocument()
    expect(within(wednesday).getByText('Side quest')).toBeInTheDocument()
    expect(within(column('Wednesday')).getByRole('button', { name: 'Swap Wednesday' })).toBeInTheDocument()
  })

  it('says a synced plan is on Google Calendar, and how long ago it was pushed', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('synced')), ...lists })
    await renderPage()

    const status = await screen.findByText('Synced to Google Calendar 2 min ago')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
    // The time is kept current.
    act(() => vi.advanceTimersByTime(58 * 60_000))
    expect(status).toHaveTextContent('Synced to Google Calendar 1 hr ago')
  })

  it("shows a failed read of the week's shopping list with Retry, the action still opening the Supply run", async () => {
    let failing = true
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      'GET /api/v1/shopping/2026-10-05': () => (failing ? errorResponse(500, 'internal') : jsonResponse(200, supplyRun(28))),
      ...lists,
    })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load this week's shopping list. internal message")
    expect(screen.getByText('Spaghetti bolognese')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Shopping list' })).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    failing = false
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    const shopping = await screen.findByRole('link', { name: 'Shopping list · 28' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    fireEvent.click(shopping)
    expect(await screen.findByText('Opened /plan/shopping?week=2026-10-05')).toBeInTheDocument()
  })

  it("opens the week's Supply run from the Shopping list action, saying how many items are still to buy", async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      [`GET ${NEXT_WEEK}`]: () => errorResponse(404, 'not_found'),
      'GET /api/v1/shopping/2026-10-05': () => jsonResponse(200, supplyRun(28)),
      ...lists,
    })
    await renderPage()

    const shopping = await screen.findByRole('link', { name: 'Shopping list · 28' })
    expect(shopping).toHaveAttribute('href', '/plan/shopping?week=2026-10-05')
    expect(declaredStyle(shopping).borderColor).toBe('var(--color-neutral-300)')
    // Another week's action opens that week's, which has no list yet.
    fireEvent.click(screen.getByRole('button', { name: 'Next week' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Oct 12 – 18' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Shopping list' })).toHaveAttribute('href', '/plan/shopping?week=2026-10-12')
    fireEvent.click(screen.getByRole('button', { name: 'Previous week' }))
    fireEvent.click(await screen.findByRole('link', { name: 'Shopping list · 28' }))
    expect(await screen.findByText('Opened /plan/shopping?week=2026-10-05')).toBeInTheDocument()
  })

  it('says changes to a pushed plan are not on Google Calendar', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('changed')), ...lists })
    await renderPage()

    expect(await screen.findByText('Changes not pushed to Google Calendar')).toBeInTheDocument()
  })

  it('pushes the plan from the header, showing progress, then the plan as synced', async () => {
    const answer = deferred()
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('changed')),
      [`POST ${PUSH}`]: () => answer.response,
      ...lists,
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Push to Calendar' }))
    const pushing = await screen.findByRole('button', { name: 'Pushing…' })
    expect(pushing).toBeDisabled()
    expect(pushing).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeDisabled()

    answer.send(jsonResponse(200, syncedNow()))
    const status = await screen.findByText('Synced to Google Calendar just now')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
    expect(screen.getByRole('button', { name: 'Push to Calendar' })).toBeEnabled()
  })

  it('links a google_reconnect_required push failure to the Google Calendar settings', async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('synced')),
      [`POST ${PUSH}`]: () => errorResponse(409, 'google_reconnect_required'),
      ...lists,
    })
    await renderPage()

    await screen.findByText('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Push to Calendar' }))
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByRole('link', { name: 'Reconnect Google Calendar' })).toHaveAttribute('href', '/settings/calendar')
    expect(screen.getByText('Synced to Google Calendar 2 min ago')).toBeInTheDocument()
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
      // The 6-dot grip, last in the row's button.
      expect(within(orzo).getByRole('button').lastElementChild!.children).toHaveLength(6)

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
    expect(within(friday).getByText('Stovetop · 25 min')).toBeInTheDocument()
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
  it('reads the Quest log again once a draft finishes, dropping the dinners it planned', async () => {
    let plan = planBody('draft')
    let job = draftJob('running')
    let questLog = [dinner('q1', 'Lemon orzo', 'easy', 25)]
    stubServer({
      ...lists,
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, plan),
      [`POST ${DRAFT}`]: () => jsonResponse(202, { jobId: 'j1' }),
      [`GET ${JOB}`]: () => jsonResponse(200, job),
      [`GET ${QUEST_LOG}`]: () => jsonResponse(200, { dinners: questLog }),
    })
    await renderPage()

    const list = await screen.findByRole('list', { name: layout === 'desktop' ? 'Quest log' : 'Quest log dinners' })
    expect(await within(list).findByText('Lemon orzo')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Rebuild week' }))
    expect(await screen.findByRole('region', { name: 'Drafting this week' })).toBeInTheDocument()

    plan = planBody('draft', ['Lemon orzo', ...NAMES.slice(1)])
    questLog = []
    job = draftJob('succeeded')
    const week = await screen.findByRole('list', { name: 'Dinners this week' }, { timeout: 3000 })
    expect(await within(week).findByText('Lemon orzo', {}, { timeout: 3000 })).toBeInTheDocument()
    await waitFor(() => expect(within(list).queryByText('Lemon orzo')).not.toBeInTheDocument())
  })
})

// Dragging a dinner from desktop's panel or tablet's strip onto a day swaps
// it in, by pointer or keyboard.
describe.each([
  { width: 1180, layout: 'tablet' },
  { width: 1440, layout: 'desktop' },
])('PlanPage drag and drop at $width px', ({ width, layout }) => {
  const FRIDAY = `${THIS_WEEK}/days/2026-10-09`
  let capture: Mock<(pointerId: number) => void>
  // The element document.elementFromPoint finds under the pointer.
  let hovered: Element | null = null

  // jsdom has no pointer capture and no layout to hit-test.
  beforeEach(() => {
    mockViewport(width)
    capture = vi.fn()
    Element.prototype.setPointerCapture = capture
    hovered = null
    document.elementFromPoint = () => hovered
  })

  afterEach(() => {
    Reflect.deleteProperty(Element.prototype, 'setPointerCapture')
    Reflect.deleteProperty(document, 'elementFromPoint')
  })

  // The Quest log has lemon orzo and miso salmon.
  const dragLists = {
    ...lists,
    [`GET ${QUEST_LOG}`]: () =>
      jsonResponse(200, { dinners: [dinner('q1', 'Lemon orzo', 'easy', 25), dinner('q2', 'Miso salmon', 'medium', 30)] }),
  }

  // swappedBody is the plan as the server answers a swap of Friday to lemon
  // orzo, a Quest-log pick.
  function swappedBody() {
    const body = planBody('draft')
    body.days[4] = { ...body.days[4]!, dinner: { ...body.days[4]!.dinner, id: 'q1', name: 'Lemon orzo' }, is_new: true }
    return body
  }

  function puts(fetchMock: Mock<typeof fetch>) {
    return fetchMock.mock.calls
      .filter(([, init]) => init?.method === 'PUT')
      .map(([input, init]) => `${String(input)} ${String(init?.body)}`)
  }

  function column(day: string) {
    return within(screen.getByRole('list', { name: 'Dinners this week' })).getByRole('listitem', { name: day })
  }

  // quest is the Quest-log dinner's draggable: a row of the panel on
  // desktop, a chip of the strip on tablet.
  async function quest(name: string) {
    const list = await screen.findByRole('list', { name: layout === 'desktop' ? 'Quest log' : 'Quest log dinners' })
    return within(list).getByRole('button', { name: new RegExp(name) })
  }

  const announcer = () => screen.getByTestId('drag-announcer')
  const float = () => screen.queryByTestId('drag-float')

  // press, move and release drive pointer 3 on the draggable: down at
  // (100, 100), moved over an element (or none) at (160, 140) or the given
  // point, and let go there.
  function press(draggable: Element) {
    fireEvent.pointerDown(draggable, { pointerId: 3, isPrimary: true, button: 0, clientX: 100, clientY: 100 })
  }
  function move(draggable: Element, over: Element | null, [clientX, clientY] = [160, 140]) {
    hovered = over
    fireEvent.pointerMove(draggable, { pointerId: 3, isPrimary: true, clientX, clientY })
  }
  function release(draggable: Element) {
    fireEvent.pointerUp(draggable, { pointerId: 3, isPrimary: true, button: 0, clientX: 160, clientY: 140 })
  }

  it('drops a dinner dragged onto a day, swapping it in at once and landing the plan the server answers', async () => {
    let answer!: (response: Response) => void
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      ...dragLists,
      [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (answer = resolve)),
    })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')
    expect(declaredStyle(orzo).getPropertyValue('touch-action')).toBe('none')
    expect(orzo).toHaveAccessibleDescription(
      'Press Enter or Space to pick it up, the arrow keys to choose a day, Enter to drop it on that day and Escape to cancel.',
    )

    press(orzo)
    expect(capture).toHaveBeenCalledExactlyOnceWith(3)
    expect(capture.mock.contexts[0]).toBe(orzo)
    // A press alone is no drag.
    expect(float()).not.toBeInTheDocument()

    // Over Friday: the source a dashed ghost at 55%, its copy floating with
    // the large shadow at -3deg, and Friday the drop target.
    move(orzo, within(column('Friday')).getByText('Pizza night'))
    expect(declaredStyle(orzo).opacity).toBe('0.55')
    expect(declaredStyle(orzo).borderStyle).toBe('dashed')
    const copy = float()!
    expect(copy).toHaveTextContent('Lemon orzo')
    expect(copy).toHaveAttribute('aria-hidden', 'true')
    expect(declaredStyle(copy).boxShadow).toBe('var(--shadow-lg)')
    expect(declaredStyle(copy).transform).toBe('rotate(-3deg)')
    expect(declaredStyle(copy).pointerEvents).toBe('none')
    // Offset 48 px below right of the pointer at (160, 140), clear of the
    // day's text under it.
    expect(copy.style.left).toBe('208px')
    expect(copy.style.top).toBe('188px')
    expect(announcer()).toHaveTextContent('Picked up Lemon orzo.')
    const target = within(column('Friday')).getByText('Drop to swap').parentElement!
    expect(target).toHaveTextContent('Drop to swapReplaces Pizza night')
    expect(declaredStyle(target).border).toBe('2.5px dashed var(--color-accent)')
    expect(declaredStyle(target).background).toBe('var(--color-accent-100)')
    expect(within(column('Thursday')).queryByText('Drop to swap')).not.toBeInTheDocument()

    // Moving on to Thursday moves the target.
    move(orzo, column('Thursday'))
    expect(within(column('Friday')).queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(within(column('Thursday')).getByText('Replaces Chicken stir-fry')).toBeInTheDocument()

    move(orzo, within(column('Friday')).getByRole('button', { name: 'Swap Friday' }))
    release(orzo)
    expect(float()).not.toBeInTheDocument()
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(declaredStyle(orzo).opacity).toBe('')
    expect(announcer()).toHaveTextContent('Dropped Lemon orzo on Friday, replacing Pizza night.')

    // Optimistic: Friday is the Side quest at once.
    const friday = await waitFor(() => within(column('Friday')).getByText('Lemon orzo').parentElement!)
    expect(within(friday).getByText('Side quest')).toBeInTheDocument()
    await waitFor(() => expect(puts(fetchMock)).toEqual([`${FRIDAY} {"dinner_id":"q1"}`]))

    await act(async () => answer(jsonResponse(200, swappedBody())))
    expect(within(column('Friday')).getByText('Lemon orzo')).toBeInTheDocument()
    expect(screen.queryByText('Pizza night')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(puts(fetchMock)).toHaveLength(1)
  })

  it('flips the floating copy to the left of and above the pointer where it would leave the viewport', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...dragLists })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')
    vi.spyOn(orzo, 'getBoundingClientRect').mockReturnValue(DOMRect.fromRect({ x: 90, y: 90, width: 240, height: 60 }))

    press(orzo)
    const [x, y] = [window.innerWidth - 100, window.innerHeight - 40]
    move(orzo, document.body, [x, y])
    const copy = float()!
    expect(copy.style.width).toBe('240px')
    expect(copy.style.left).toBe(`${x - 48 - 240}px`)
    expect(copy.style.top).toBe(`${y - 48 - 60}px`)
    release(orzo)
  })

  it('cancels a drag let go over no day, or cancelled, with no swap', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...dragLists })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')

    press(orzo)
    move(orzo, column('Friday'))
    expect(within(column('Friday')).getByText('Drop to swap')).toBeInTheDocument()
    // Off the grid, onto the page around it.
    move(orzo, document.body)
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(float()).toBeInTheDocument()
    release(orzo)
    expect(float()).not.toBeInTheDocument()
    expect(declaredStyle(orzo).opacity).toBe('')
    expect(announcer()).toHaveTextContent('Cancelled. Lemon orzo was not swapped in.')

    // A cancelled pointer, over a day, is no drop either.
    press(orzo)
    move(orzo, column('Monday'))
    expect(within(column('Monday')).getByText('Replaces Beef tacos')).toBeInTheDocument()
    fireEvent.pointerCancel(orzo, { pointerId: 3, isPrimary: true })
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(float()).not.toBeInTheDocument()

    // Nor is a drag Escape ends.
    press(orzo)
    move(orzo, column('Monday'))
    fireEvent.keyDown(orzo, { key: 'Escape' })
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    release(orzo)

    expect(within(column('Friday')).getByText('Pizza night')).toBeInTheDocument()
    expect(within(column('Monday')).getByText('Beef tacos')).toBeInTheDocument()
    expect(puts(fetchMock)).toEqual([])
  })

  it('puts the day back and shows the failure when the dropped swap is refused', async () => {
    let refuse!: () => void
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      ...dragLists,
      [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (refuse = () => resolve(errorResponse(409, 'dinner_not_plannable')))),
    })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')

    press(orzo)
    move(orzo, column('Friday'))
    release(orzo)
    await waitFor(() => expect(within(column('Friday')).getByText('Lemon orzo')).toBeInTheDocument())
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(1))

    // While the swap is pending no other drag starts, by pointer or keyboard.
    const salmon = await quest('Miso salmon')
    expect(salmon).toHaveAttribute('aria-disabled', 'true')
    press(salmon)
    move(salmon, column('Monday'))
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(float()).not.toBeInTheDocument()
    release(salmon)
    fireEvent.keyDown(salmon, { key: 'Enter' })
    expect(announcer()).toHaveTextContent('A swap is still being saved. Pick Miso salmon up once it is done.')
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()

    await act(async () => refuse())
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't swap Friday's dinner. dinner_not_plannable message")
    expect(within(column('Friday')).getByText('Pizza night')).toBeInTheDocument()
    expect(within(column('Friday')).queryByText('Lemon orzo')).not.toBeInTheDocument()
    expect(puts(fetchMock)).toHaveLength(1)
    await waitFor(() => expect(salmon).not.toHaveAttribute('aria-disabled'))
  })

  it('swaps a dinner in with the keyboard, announcing each step', async () => {
    let answer!: (response: Response) => void
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
      ...dragLists,
      [`PUT ${FRIDAY}`]: () => new Promise<Response>((resolve) => (answer = resolve)),
    })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')
    expect(announcer()).toHaveAttribute('aria-live', 'polite')

    // Enter picks it up onto Monday.
    orzo.focus()
    fireEvent.keyDown(orzo, { key: 'Enter' })
    expect(announcer()).toHaveTextContent(
      'Picked up Lemon orzo. Monday: replaces Beef tacos. Use the arrow keys to choose a day, Enter to drop, Escape to cancel.',
    )
    expect(declaredStyle(orzo).opacity).toBe('0.55')
    expect(within(column('Monday')).getByText('Replaces Beef tacos')).toBeInTheDocument()
    // No pointer, so no floating copy.
    expect(float()).not.toBeInTheDocument()

    // The arrow keys choose the day, going no further than Monday.
    fireEvent.keyDown(orzo, { key: 'ArrowLeft' })
    expect(within(column('Monday')).getByText('Drop to swap')).toBeInTheDocument()
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    expect(announcer()).toHaveTextContent('Tuesday: replaces Spaghetti bolognese.')
    expect(within(column('Monday')).queryByText('Drop to swap')).not.toBeInTheDocument()
    fireEvent.keyDown(orzo, { key: 'ArrowDown' })
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    fireEvent.keyDown(orzo, { key: 'ArrowUp' })
    expect(announcer()).toHaveTextContent('Friday: replaces Pizza night.')
    expect(within(column('Friday')).getByText('Replaces Pizza night')).toBeInTheDocument()
    expect(puts(fetchMock)).toEqual([])

    // Enter drops it, swapping it in at once; focus moves to the day.
    fireEvent.keyDown(orzo, { key: 'Enter' })
    expect(announcer()).toHaveTextContent('Dropped Lemon orzo on Friday, replacing Pizza night.')
    expect(screen.getByRole('button', { name: 'Swap Friday' })).toHaveFocus()
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    await waitFor(() => expect(within(column('Friday')).getByText('Lemon orzo')).toBeInTheDocument())
    await waitFor(() => expect(puts(fetchMock)).toEqual([`${FRIDAY} {"dinner_id":"q1"}`]))
    await act(async () => answer(jsonResponse(200, swappedBody())))
    expect(within(column('Friday')).getByText('Lemon orzo')).toBeInTheDocument()
    expect(puts(fetchMock)).toHaveLength(1)
  })

  it('cancels a keyboard drag with Escape, or by leaving it', async () => {
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...dragLists })
    await renderPage()
    await screen.findByText('Pizza night')
    const orzo = await quest('Lemon orzo')

    // Space picks it up too, but does not drop it.
    orzo.focus()
    fireEvent.keyDown(orzo, { key: ' ' })
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    fireEvent.keyDown(orzo, { key: ' ' })
    expect(within(column('Tuesday')).getByText('Drop to swap')).toBeInTheDocument()
    fireEvent.keyDown(orzo, { key: 'Escape' })
    expect(announcer()).toHaveTextContent('Cancelled. Lemon orzo was not swapped in.')
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    expect(declaredStyle(orzo).opacity).toBe('')
    expect(orzo).toHaveFocus()

    // The arrow keys do nothing until it is picked up again; Enter does.
    fireEvent.keyDown(orzo, { key: 'ArrowRight' })
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()
    fireEvent.keyDown(orzo, { key: 'Enter' })
    expect(within(column('Monday')).getByText('Drop to swap')).toBeInTheDocument()
    // Tabbing away cancels.
    fireEvent.blur(orzo)
    expect(announcer()).toHaveTextContent('Cancelled. Lemon orzo was not swapped in.')
    expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()

    expect(within(column('Monday')).getByText('Beef tacos')).toBeInTheDocument()
    expect(puts(fetchMock)).toEqual([])
  })

  if (layout === 'desktop') {
    it('does not pick up a Regular already this week', async () => {
      const fetchMock = stubServer({
        [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')),
        ...dragLists,
        [`PUT ${THIS_WEEK}/days/2026-10-07`]: () => new Promise<Response>(() => undefined),
      })
      await renderPage()
      await screen.findByText('Pizza night')
      fireEvent.click(await screen.findByRole('radio', { name: 'Regulars · 2' }))
      const regulars = screen.getByRole('list', { name: 'Regulars' })
      const tacos = within(regulars).getByRole('button', { name: /Beef tacos/ })
      expect(tacos).toHaveAttribute('aria-disabled', 'true')
      fireEvent.keyDown(tacos, { key: 'Enter' })
      expect(announcer()).toHaveTextContent('Beef tacos is already this week.')
      expect(screen.queryByText('Drop to swap')).not.toBeInTheDocument()

      // A Regular not this week is dragged in as one, not NEW.
      const fish = within(regulars).getByRole('button', { name: /Fish tacos/ })
      expect(fish).not.toHaveAttribute('aria-disabled')
      press(fish)
      move(fish, column('Wednesday'))
      release(fish)
      await waitFor(() => expect(within(column('Wednesday')).getByText('Fish tacos').parentElement).toHaveTextContent('Regular'))
      await waitFor(() => expect(puts(fetchMock)).toEqual([`${THIS_WEEK}/days/2026-10-07 {"dinner_id":"r1"}`]))
    })
  }
})

// Mobile's Draft board has no drag: its tiles open the swap sheet.
describe('PlanPage drag and drop on mobile', () => {
  it('has no draggables, drop targets or drag announcements', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
    const { container } = await renderPage()

    await screen.findByText('Pizza night')
    expect(container.querySelector('[data-drop-date]')).toBeNull()
    expect(screen.queryByTestId('drag-announcer')).not.toBeInTheDocument()
    expect(screen.queryByText('Drag a dinner onto a day to swap it in.')).not.toBeInTheDocument()
  })
})

// At tablet's narrow end seven usable columns do not fit beside the rail:
// the week keeps its columns usable and scrolls sideways.
describe('PlanPage at 768 px', () => {
  beforeEach(() => mockViewport(768))

  it('keeps each day column usable and scrolls the week sideways', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, planBody('draft')), ...lists })
    await renderPage()

    expect(await screen.findByText('Spaghetti bolognese')).toBeInTheDocument()
    const grid = screen.getByRole('list', { name: 'Dinners this week' })
    // Columns no narrower than 112 px; the week scrolls when seven do not
    // fit, its padding keeping the swap icons' hit boxes inside.
    expect(declaredStyle(grid).gridTemplateColumns).toBe('repeat(7, minmax(112px, 1fr))')
    expect(declaredStyle(grid).overflowX).toBe('auto')
    expect(declaredStyle(grid).padding).toBe('8px')
    expect(declaredStyle(grid).margin).toBe('-8px')

    const tuesday = within(grid).getByRole('listitem', { name: 'Tuesday' })
    // The name wraps between words, only breaking a word wider than the card.
    const name = within(tuesday).getByText('Spaghetti bolognese')
    expect(declaredStyle(name).overflowWrap).toBe('break-word')
    expect(declaredStyle(name).hyphens).toBe('')
    // The swap icon stays beside the tag.
    const footer = within(tuesday).getByRole('button', { name: 'Swap Tuesday' }).parentElement!
    expect(within(footer).getByText('Regular')).toBeInTheDocument()
    expect(declaredStyle(footer).display).toBe('flex')
    expect(declaredStyle(footer).flexWrap).toBe('')
    // Every piece of the card is still there.
    expect(within(tuesday).getByRole('img', { hidden: true, name: 'Spaghetti bolognese' })).toBeInTheDocument()
    expect(within(tuesday).getByText('Stovetop · 30 min')).toBeInTheDocument()
    expect(within(within(grid).getByRole('listitem', { name: 'Wednesday' })).getByText('Start by 1:30 pm')).toBeInTheDocument()
  })
})

describe('duration', () => {
  it.each([
    [25, '25\u00a0min'],
    [60, '1\u00a0hr'],
    [90, '1\u00a0hr\u00a030\u00a0min'],
    [360, '6\u00a0hr'],
  ])('says %i minutes as %j', (minutes, said) => {
    expect(duration(minutes)).toBe(said)
  })
})

describe('cooking', () => {
  const dinner = { id: 'd1', name: 'Tikka masala', photo_id: undefined }
  it.each([
    [{ method: 'crockpot' as const, minutes: 360 }, 'Crock pot · 6\u00a0hr'],
    [{ method: 'crockpot' as const, minutes: null }, 'Crock pot'],
    [{ method: null, minutes: 45 }, '45\u00a0min'],
    [{ method: null, minutes: null }, null],
  ])('says %o as %j', (known, said) => {
    expect(cooking({ ...dinner, ...known })).toBe(said)
  })
})

describe('ago', () => {
  const now = new Date('2026-10-07T16:00:00Z')
  it.each([
    ['2026-10-07T15:59:30Z', 'just now'],
    ['2026-10-07T15:58:00Z', '2 min ago'],
    ['2026-10-07T13:00:00Z', '3 hr ago'],
    ['2026-10-06T15:00:00Z', '1 day ago'],
    ['2026-10-04T15:00:00Z', '3 days ago'],
  ])('says %s as %j', (at, said) => {
    expect(ago(new Date(at), now)).toBe(said)
  })
})
