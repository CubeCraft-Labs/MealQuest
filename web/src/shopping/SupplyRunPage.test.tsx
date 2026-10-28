import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { DinnerDetailPage } from '../dinners/DinnerDetailPage.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import buttonStyles from '../ui/Button.module.css'
import { sentAt, SupplyRunPage } from './SupplyRunPage.tsx'

const THIS_WEEK = '/api/v1/shopping/2026-10-05'
const GENERATE = `${THIS_WEEK}/generate`
const SEND_CHAT = `${THIS_WEEK}/send-chat`
const ITEM = (id: string) => `/api/v1/shopping/items/${id}`

type Item = { id: string; name: string; qty: string | null; needed: boolean }

// The week of Oct 5's Supply run, as mock 1l starts it: Monday's tacos and
// Wednesday's tikka masala, each with one item the household has.
const GROUPS: { dinner: { id: string; name: string; status: string; meal_source: string }; date: string; day: string; items: Item[] }[] = [
  {
    dinner: { id: 'd0', name: 'Beef tacos', status: 'regular', meal_source: 'cook' },
    date: '2026-10-05',
    day: 'Monday',
    items: [
      { id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true },
      { id: 'i2', name: 'Corn tortillas', qty: '12', needed: true },
      { id: 'i3', name: 'Taco seasoning', qty: '1 packet', needed: false },
    ],
  },
  {
    dinner: { id: 'd2', name: 'Crock pot tikka masala', status: 'regular', meal_source: 'cook' },
    date: '2026-10-07',
    day: 'Wednesday',
    items: [
      { id: 'i4', name: 'Chicken thighs', qty: '2 lb', needed: true },
      { id: 'i5', name: 'Tikka masala paste', qty: '1 jar', needed: true },
      { id: 'i6', name: 'Basmati rice', qty: null, needed: false },
    ],
  },
]

// supplyRun is the week's Supply run as GET /api/v1/shopping/{weekStart}
// sends it, counted from its items.
function supplyRun(overrides: Record<string, unknown> = {}, weekStart = '2026-10-05', label = 'Week 41 · Oct 5 – 11') {
  const items = GROUPS.flatMap((group) => group.items)
  return {
    weekStart,
    label,
    title: null,
    status: 'current',
    sent_to_chat_at: null,
    chat_send_unconfirmed_at: null,
    needed_count: items.filter((item) => item.needed).length,
    total_count: items.length,
    groups: GROUPS,
    ...overrides,
  }
}

// deferred is a response the test sends when it chooses.
function deferred() {
  let send!: (response: Response) => void
  const response = new Promise<Response>((resolve) => {
    send = resolve
  })
  return { response, send }
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

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

// writes lists the PATCHes and POSTs sent, as "METHOD path body".
function writes(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === 'PATCH' || init?.method === 'POST')
    .map(([input, init]) => `${init!.method} ${String(input)} ${String(init?.body)}`)
}

// Where shows the path the router is at.
function Where() {
  return <output aria-label="Path">{useLocation().pathname}</output>
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the Supply run at path, routed beside the dinner detail screen a card
// opens.
async function renderPage(path = '/plan/shopping') {
  const api = createApiClient(fakeLocation('/plan/shopping'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/dinner/:id" element={<DinnerDetailPage />} />
            <Route path="*" element={<SupplyRunPage />} />
          </Routes>
          <Where />
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

function checkbox(name: string) {
  return screen.getByRole('checkbox', { name })
}

function row(name: string) {
  return screen.getByText(name).closest('li')!
}

describe('SupplyRunPage', () => {
  it('renders mock 1l: kicker, title, the count to buy of all items and the helper', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()) })
    await renderPage()

    const title = await screen.findByRole('heading', { level: 1, name: 'Shopping list' })
    expect(declaredStyle(title).fontFamily).toBe('var(--font-heading)')
    // The kicker is the week's dates, set in capitals by the stylesheet.
    const kicker = screen.getByText('Supply run · Oct 5 – 11')
    expect(declaredStyle(kicker).textTransform).toBe('uppercase')
    expect(declaredStyle(kicker).color).toBe('var(--color-accent-700)')
    expect(screen.getByText('4 / 6')).toBeInTheDocument()
    expect(screen.getByText("Uncheck anything you already have — it won't be counted.")).toBeInTheDocument()
    expect(screen.queryByText('Archived')).not.toBeInTheDocument()
  })

  it('shows one surface card per dinner with its Bagel name and day', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()) })
    await renderPage()

    const tacos = await screen.findByRole('region', { name: 'Beef tacos' })
    expect(screen.getAllByRole('region')).toHaveLength(2)
    expect(declaredStyle(tacos).borderRadius).toBe('24px')
    expect(declaredStyle(tacos).background).toBe('var(--color-surface)')
    const name = within(tacos).getByRole('heading', { level: 2, name: 'Beef tacos' })
    expect(declaredStyle(name).fontFamily).toBe('var(--font-heading)')
    expect(declaredStyle(name).fontSize).toBe('16px')
    expect(declaredStyle(within(tacos).getByText('MON')).fontSize).toBe('11px')
    expect(within(screen.getByRole('region', { name: 'Crock pot tikka masala' })).getByText('WED')).toBeInTheDocument()
  })

  it("opens each dinner's recipe, its ingredients and steps, from its name", async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      'GET /api/v1/dinners/d2': () =>
        jsonResponse(200, {
          id: 'd2',
          name: 'Crock pot tikka masala',
          status: 'regular',
          origin: 'regular',
          method: 'crockpot',
          minutes_total: 360,
          difficulty: 'easy',
          cuisine: null,
          is_favorite: false,
          meal_source: 'cook',
          photo_urls: null,
          ingredients: [
            { name: 'Chicken thighs', qty: '2', unit: 'lb', note: null, raw_text: '2 lb chicken thighs' },
            { name: 'Tikka masala paste', qty: '1', unit: 'jar', note: null, raw_text: '1 jar tikka masala paste' },
          ],
          steps: ['Put everything in the crock pot.', 'Cook on low for 6 hours.'],
          source: null,
          grounding_status: 'manual',
          grounding_error: null,
          why_picked: null,
        }),
    })
    await renderPage()

    const tacos = await screen.findByRole('region', { name: 'Beef tacos' })
    // The dinner's name is the card's one way to its recipe: no Recipe pill.
    expect(within(tacos).getAllByRole('button')).toHaveLength(1)
    expect(within(tacos).queryByRole('link')).not.toBeInTheDocument()
    const tacosName = within(within(tacos).getByRole('heading', { level: 2, name: 'Beef tacos' })).getByRole('button', { name: 'Beef tacos' })
    expect(declaredStyle(tacosName).color).toBe('inherit')
    expect(declaredStyle(tacosName).textDecoration).toBe('none')
    expectMinHitArea(tacosName)
    expect(screen.queryByRole('link', { name: /Recipe/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Recipe/ })).not.toBeInTheDocument()

    const tikka = screen.getByRole('region', { name: 'Crock pot tikka masala' })
    fireEvent.click(within(tikka).getByRole('button', { name: 'Crock pot tikka masala' }))
    await waitFor(() => expect(screen.getByRole('status', { name: 'Path' })).toHaveTextContent('/dinner/d2'))
    expect(await screen.findByRole('heading', { level: 2, name: 'Ingredients' })).toBeInTheDocument()
    const [ingredients, steps] = screen.getAllByRole('list')
    expect(within(ingredients!).getAllByRole('listitem').map((item) => item.firstElementChild!.textContent)).toEqual([
      'Chicken thighs',
      'Tikka masala paste',
    ])
    expect(screen.getByRole('heading', { level: 2, name: 'Steps' })).toBeInTheDocument()
    expect(within(steps!).getAllByRole('listitem').map((step) => step.textContent)).toEqual([
      '1Put everything in the crock pot.',
      '2Cook on low for 6 hours.',
    ])
  })

  it('keeps an item toggle on the card, not opening the recipe', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i1')}`]: () => jsonResponse(200, { id: 'i1', needed: false, needed_count: 3, total_count: 6 }),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Ground beef' }))
    await waitFor(() => expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i1')} {"needed":false}`]))
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(screen.getByRole('status', { name: 'Path' })).toHaveTextContent('/plan/shopping')
  })

  it("links a frozen Supply run's dinner names to their recipes too", async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ status: 'archived' })) })
    await renderPage()

    const tacos = await screen.findByRole('region', { name: 'Beef tacos' })
    expect(within(tacos).getByRole('button', { name: 'Beef tacos' })).toBeInTheDocument()
  })

  it('shows a dinner removed from Regulars since by name only, its card still working', async () => {
    const archivedTacos = { ...GROUPS[0]!, dinner: { ...GROUPS[0]!.dinner, status: 'archived' } }
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ groups: [archivedTacos, GROUPS[1]] })),
      [`PATCH ${ITEM('i1')}`]: () => jsonResponse(200, { id: 'i1', needed: false, needed_count: 3, total_count: 6 }),
    })
    await renderPage()

    const tacos = await screen.findByRole('region', { name: 'Beef tacos' })
    expect(within(tacos).getByRole('heading', { level: 2, name: 'Beef tacos' })).toBeInTheDocument()
    expect(within(tacos).queryByRole('button', { name: 'Beef tacos' })).not.toBeInTheDocument()
    expect(within(tacos).queryByRole('link')).not.toBeInTheDocument()
    expect(
      within(screen.getByRole('region', { name: 'Crock pot tikka masala' })).getByRole('button', { name: 'Crock pot tikka masala' }),
    ).toBeInTheDocument()

    fireEvent.click(within(tacos).getByText('Beef tacos'))
    expect(screen.getByRole('status', { name: 'Path' })).toHaveTextContent('/plan/shopping')
    fireEvent.click(within(tacos).getByRole('checkbox', { name: 'Ground beef' }))
    await waitFor(() => expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i1')} {"needed":false}`]))
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(screen.getByRole('status', { name: 'Path' })).toHaveTextContent('/plan/shopping')
  })

  it('refuses a Supply run whose dinner status is unknown', async () => {
    const unknown = { ...GROUPS[0]!, dinner: { ...GROUPS[0]!.dinner, status: 'retired' } }
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ groups: [unknown, GROUPS[1]] })) })
    await renderPage()

    await screen.findByRole('alert')
    expect(screen.queryByRole('region', { name: 'Beef tacos' })).not.toBeInTheDocument()
  })

  it('checks the items to buy; one the household has is unchecked, struck through and "have it"', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()) })
    await renderPage()

    expect(await screen.findByRole('checkbox', { name: 'Ground beef' })).toBeChecked()
    expect(within(row('Ground beef')).getByText('1 lb')).toBeInTheDocument()
    expect(declaredStyle(screen.getByText('Ground beef')).textDecoration).toBe('')

    expect(checkbox('Taco seasoning')).not.toBeChecked()
    expect(within(row('Taco seasoning')).getByText('have it')).toBeInTheDocument()
    expect(within(row('Taco seasoning')).queryByText('1 packet')).not.toBeInTheDocument()
    expect(declaredStyle(screen.getByText('Taco seasoning')).textDecoration).toBe('line-through')
    expect(screen.getAllByText('have it')).toHaveLength(2)
  })

  it('toggles an item at once, then shows the counts the server answers with', async () => {
    const answer = deferred()
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i3')}`]: () => answer.response,
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    // At once, before the server answers: checked, no longer "have it", and counted.
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(within(row('Taco seasoning')).getByText('1 packet')).toBeInTheDocument()
    expect(screen.getByText('5 / 6')).toBeInTheDocument()
    await waitFor(() => expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i3')} {"needed":true}`]))
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'PATCH')!
    expect(new Headers(call[1]?.headers).get('X-MealQuest-CSRF')).toBe(ME.csrfToken)

    // The server's counts are the ones shown.
    answer.send(jsonResponse(200, { id: 'i3', needed: true, needed_count: 5, total_count: 7 }))
    expect(await screen.findByText('5 / 7')).toBeInTheDocument()
    expect(checkbox('Taco seasoning')).toBeChecked()
  })

  it('marks an item "have it" by unchecking it', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i1')}`]: () => jsonResponse(200, { id: 'i1', needed: false, needed_count: 3, total_count: 6 }),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Ground beef' }))
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(within(row('Ground beef')).getByText('have it')).toBeInTheDocument()
    expect(screen.getByText('3 / 6')).toBeInTheDocument()
    await waitFor(() => expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i1')} {"needed":false}`]))
  })

  it('puts a refused toggle back, item and count, and says why with Retry', async () => {
    const answer = deferred()
    let patches = 0
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i3')}`]: () => {
        patches++
        return patches === 1 ? answer.response : jsonResponse(200, { id: 'i3', needed: true, needed_count: 5, total_count: 6 })
      },
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(screen.getByText('5 / 6')).toBeInTheDocument()

    answer.send(errorResponse(500, 'internal'))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Couldn\'t mark Taco seasoning to buy. internal message')
    expect(checkbox('Taco seasoning')).not.toBeChecked()
    expect(within(row('Taco seasoning')).getByText('have it')).toBeInTheDocument()
    expect(screen.getByText('4 / 6')).toBeInTheDocument()

    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(checkbox('Taco seasoning')).toBeChecked()
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    expect(screen.getByText('5 / 6')).toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i3')} {"needed":true}`, `PATCH ${ITEM('i3')} {"needed":true}`])
  })

  it('sends toggles one at a time and keeps the count true when one between them fails', async () => {
    const first = deferred()
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i3')}`]: () => first.response,
      [`PATCH ${ITEM('i6')}`]: () => jsonResponse(200, { id: 'i6', needed: true, needed_count: 5, total_count: 6 }),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    fireEvent.click(checkbox('Basmati rice'))
    expect(screen.getByText('6 / 6')).toBeInTheDocument()
    // The second waits for the first.
    await waitFor(() => expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i3')} {"needed":true}`]))

    first.send(errorResponse(500, 'internal'))
    await waitFor(() => expect(writes(fetchMock)).toHaveLength(2))
    await waitFor(() => expect(screen.getByText('5 / 6')).toBeInTheDocument())
    expect(checkbox('Taco seasoning')).not.toBeChecked()
    expect(checkbox('Basmati rice')).toBeChecked()
  })

  it('keeps a refused toggle shown when a later toggle of another item succeeds, until it is retried', async () => {
    let patches = 0
    stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`PATCH ${ITEM('i3')}`]: () => {
        patches++
        return patches === 1 ? errorResponse(500, 'internal') : jsonResponse(200, { id: 'i3', needed: true, needed_count: 6, total_count: 6 })
      },
      [`PATCH ${ITEM('i6')}`]: () => jsonResponse(200, { id: 'i6', needed: true, needed_count: 5, total_count: 6 }),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't mark Taco seasoning to buy. internal message")
    expect(checkbox('Taco seasoning')).not.toBeChecked()

    fireEvent.click(checkbox('Basmati rice'))
    await waitFor(() => expect(screen.getByText('5 / 6')).toBeInTheDocument())
    expect(checkbox('Basmati rice')).toBeChecked()
    // The refusal is still shown: nothing has superseded it.
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't mark Taco seasoning to buy. internal message")
    expect(checkbox('Taco seasoning')).not.toBeChecked()

    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.getByText('6 / 6')).toBeInTheDocument())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(checkbox('Taco seasoning')).toBeChecked()
  })

  it('reads the list again when a toggle is refused because the week was archived, and shows it read-only', async () => {
    let reads = 0
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => {
        reads++
        return jsonResponse(200, supplyRun(reads === 1 ? {} : { status: 'archived' }))
      },
      [`PATCH ${ITEM('i3')}`]: () => errorResponse(409, 'archived'),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(reads).toBe(2)
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    expect(within(row('Taco seasoning')).getByText('have it')).toBeInTheDocument()
    expect(screen.getByText('4 / 6')).toBeInTheDocument()
    expect(screen.getByText('This list is in the Supply archive, as it was when the week ended.')).toBeInTheDocument()
    // It says why, with nothing to retry.
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't mark Taco seasoning to buy. archived message")
    expect(within(alert).queryByRole('button')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('Taco seasoning'))
    expect(writes(fetchMock)).toEqual([`PATCH ${ITEM('i3')} {"needed":true}`])
  })

  it('shows the status line once the list was sent to Google Chat', async () => {
    // Saturday Oct 10, 8:00 am in New York.
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:00:00Z' })) })
    await renderPage()

    const status = await screen.findByText('Sent to Google Chat · Sat 8:00 am')
    expect(declaredStyle(status).color).toBe('var(--color-accent-2-800)')
  })

  it('has no status line until the list is sent', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()) })
    await renderPage()

    await screen.findByText('4 / 6')
    expect(screen.queryByText(/Sent to Google Chat/)).not.toBeInTheDocument()
  })

  it('stacks the cards on mobile, with a link to the Supply archive and no header actions', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()) })
    await renderPage()

    await screen.findByText('4 / 6')
    const groups = screen.getByTestId('supply-run-groups')
    expect(declaredStyle(groups).flexDirection).toBe('column')
    expect(declaredStyle(groups).columnCount).toBe('')
    expect(screen.getByRole('link', { name: 'Supply archive' })).toHaveAttribute('href', '/plan/shopping/archive')
    expect(screen.queryByRole('button', { name: 'Send to Google Chat' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Archive' })).not.toBeInTheDocument()
  })

  it.each([
    ['tablet (mock 2b)', 1180],
    ['desktop (mock 2f)', 1440],
  ])('lays the cards out in 3 masonry columns on %s, with Archive and Send to Google Chat', async (_layout, width) => {
    mockViewport(width)
    const fetchMock = stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:00:00Z' })) })
    await renderPage()

    await screen.findByText('4 / 6')
    const groups = screen.getByTestId('supply-run-groups')
    expect(declaredStyle(groups).columnCount).toBe('3')
    expect(declaredStyle(screen.getByRole('region', { name: 'Beef tacos' })).breakInside).toBe('avoid')

    const archive = screen.getByRole('link', { name: 'Archive' })
    expect(archive).toHaveAttribute('href', '/plan/shopping/archive')
    expect(archive).toHaveClass(buttonStyles.outline!)

    // Already sent, the primary action sends it again.
    const send = screen.getByRole('button', { name: 'Send again' })
    expect(send).toHaveClass(buttonStyles.primary!)
    expect(send).toBeEnabled()
    expect(writes(fetchMock)).toEqual([])

    expect(screen.getByText('Sent to Google Chat · Sat 8:00 am')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Supply archive' })).not.toBeInTheDocument()
  })

  it('sends the list to Google Chat once and shows when it was sent', async () => {
    mockViewport(1180)
    const sent = deferred()
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`POST ${SEND_CHAT}`]: () => sent.response,
    })
    await renderPage()

    await screen.findByText('4 / 6')
    expect(screen.queryByText(/Sent to Google Chat ·/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Send to Google Chat' }))

    const sending = await screen.findByRole('button', { name: 'Sending…' })
    expect(sending).toBeDisabled()
    expect(writes(fetchMock)).toEqual([`POST ${SEND_CHAT} {"resend":false}`])
    // Saturday Oct 10, 8:00 am in New York.
    sent.send(jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:00:00Z' })))

    expect(await screen.findByText('Sent to Google Chat · Sat 8:00 am')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send again' })).toBeEnabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('sends a list already sent again with resend', async () => {
    mockViewport(1440)
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:00:00Z' })),
      [`POST ${SEND_CHAT}`]: () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T16:30:00Z' })),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Send again' }))

    expect(await screen.findByText('Sent to Google Chat · Sat 12:30 pm')).toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([`POST ${SEND_CHAT} {"resend":true}`])
  })

  it('says why a send to Google Chat failed, and retries it', async () => {
    mockViewport(1180)
    const answers = [
      () => errorResponse(409, 'not_configured'),
      () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:00:00Z' })),
    ]
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun()),
      [`POST ${SEND_CHAT}`]: () => answers.shift()!(),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Send to Google Chat' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't send the shopping list to Google Chat. not_configured message")
    expect(screen.queryByText(/Sent to Google Chat ·/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send to Google Chat' })).toBeEnabled()

    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Sent to Google Chat · Sat 8:00 am')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([`POST ${SEND_CHAT} {"resend":false}`, `POST ${SEND_CHAT} {"resend":false}`])
  })

  it('says a send whose outcome is not known was not confirmed, offers no retry, and sends again only with resend', async () => {
    mockViewport(1180)
    let run = supplyRun()
    const answers = [
      () => {
        // Saturday Oct 10, 8:00 am in New York.
        run = supplyRun({ chat_send_unconfirmed_at: '2026-10-10T12:00:00Z' })
        return errorResponse(502, 'chat_unconfirmed')
      },
      () => jsonResponse(200, supplyRun({ sent_to_chat_at: '2026-10-10T12:05:00Z' })),
    ]
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => jsonResponse(200, run),
      [`POST ${SEND_CHAT}`]: () => answers.shift()!(),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Send to Google Chat' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't send the shopping list to Google Chat. chat_unconfirmed message")
    expect(within(alert).queryByRole('button')).not.toBeInTheDocument()
    // The Supply run is read again, and says the send is unconfirmed.
    expect(
      await screen.findByText('Send to Google Chat not confirmed · Sat 8:00 am — check the space before sending again'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Sent to Google Chat ·/)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Send again' }))
    expect(await screen.findByText('Sent to Google Chat · Sat 8:05 am')).toBeInTheDocument()
    expect(screen.queryByText(/not confirmed/)).not.toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([`POST ${SEND_CHAT} {"resend":false}`, `POST ${SEND_CHAT} {"resend":true}`])
  })

  it('shows an unconfirmed send on mobile', async () => {
    stubServer({ [`GET ${THIS_WEEK}`]: () => jsonResponse(200, supplyRun({ chat_send_unconfirmed_at: '2026-10-10T12:00:00Z' })) })
    await renderPage()

    expect(
      await screen.findByText('Send to Google Chat not confirmed · Sat 8:00 am — check the space before sending again'),
    ).toBeInTheDocument()
  })

  it('renders an archived list read-only, tagged Archived', async () => {
    const fetchMock = stubServer({
      'GET /api/v1/shopping/2026-09-28': () =>
        jsonResponse(200, supplyRun({ status: 'archived' }, '2026-09-28', 'Week 40 · Sep 28 – Oct 4')),
    })
    await renderPage('/plan/shopping?week=2026-09-28')

    expect(await screen.findByText('Supply run · Sep 28 – Oct 4')).toBeInTheDocument()
    expect(screen.getByText('Archived')).toBeInTheDocument()
    expect(screen.getByText('This list is in the Supply archive, as it was when the week ended.')).toBeInTheDocument()
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    // The marks still show what was bought and what was on hand.
    expect(within(row('Ground beef')).getByText('1 lb')).toBeInTheDocument()
    expect(within(row('Taco seasoning')).getByText('have it')).toBeInTheDocument()
    expect(declaredStyle(screen.getByText('Taco seasoning')).textDecoration).toBe('line-through')
    expect(screen.getByText('4 / 6')).toBeInTheDocument()

    fireEvent.click(screen.getByText('Taco seasoning'))
    expect(screen.getByText('4 / 6')).toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([])
  })

  it('offers no Send to Google Chat for an archived list on tablet', async () => {
    mockViewport(1180)
    stubServer({
      'GET /api/v1/shopping/2026-09-28': () =>
        jsonResponse(200, supplyRun({ status: 'archived' }, '2026-09-28', 'Week 40 · Sep 28 – Oct 4')),
    })
    await renderPage('/plan/shopping?week=2026-09-28')

    await screen.findByText('Archived')
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    expect(screen.queryByRole('button', { name: 'Send to Google Chat' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Archive' })).toBeInTheDocument()
  })

  it('refuses a week that is not a Monday', async () => {
    const fetchMock = stubServer({})
    await renderPage('/plan/shopping?week=2026-09-29')

    expect(await screen.findByRole('alert')).toHaveTextContent('“2026-09-29” is not the Monday of a week, as YYYY-MM-DD.')
    expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith('/api/v1/shopping'))).toEqual([])
  })

  it('shows skeleton cards while the list loads', async () => {
    const answer = deferred()
    stubServer({ [`GET ${THIS_WEEK}`]: () => answer.response })
    await renderPage()

    expect(await screen.findByRole('region', { name: 'Shopping list loading' })).toHaveAttribute('aria-busy', 'true')
    await waitFor(() => expect(screen.getAllByTestId('skeleton-group')).toHaveLength(3))
    answer.send(jsonResponse(200, supplyRun()))
    expect(await screen.findByText('4 / 6')).toBeInTheDocument()
    expect(screen.queryByTestId('skeleton-group')).not.toBeInTheDocument()
  })

  it('says when the list cannot load, and Retry reads it again', async () => {
    let reads = 0
    stubServer({
      [`GET ${THIS_WEEK}`]: () => {
        reads++
        return reads === 1 ? errorResponse(500, 'internal') : jsonResponse(200, supplyRun())
      },
    })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load the shopping list. internal message")
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('4 / 6')).toBeInTheDocument()
  })

  it('offers to make the list when the week has none, and shows the list made', async () => {
    const fetchMock = stubServer({
      [`GET ${THIS_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`POST ${GENERATE}`]: () => jsonResponse(200, { ...supplyRun(), skipped: [] }),
    })
    await renderPage()

    expect(await screen.findByRole('heading', { name: 'No shopping list yet' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Make the shopping list' }))
    expect(await screen.findByText('4 / 6')).toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([`POST ${GENERATE} undefined`])
  })

  it('says why the list could not be made', async () => {
    stubServer({
      [`GET ${THIS_WEEK}`]: () => errorResponse(404, 'not_found'),
      [`POST ${GENERATE}`]: () => errorResponse(404, 'not_found'),
    })
    await renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Make the shopping list' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't make the shopping list. not_found message")
    expect(screen.getByRole('heading', { name: 'No shopping list yet' })).toBeInTheDocument()
  })
})

describe('sentAt', () => {
  it('says the day and time in the household zone', () => {
    expect(sentAt(new Date('2026-10-10T12:00:00Z'), 'America/New_York')).toBe('Sat 8:00 am')
    expect(sentAt(new Date('2026-10-11T03:30:00Z'), 'America/New_York')).toBe('Sat 11:30 pm')
  })
})
