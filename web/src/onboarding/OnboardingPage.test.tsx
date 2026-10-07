import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { createApiClient, createVerifiedSession } from '../api/index.ts'
import App from '../App.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { dinnerBody, type DinnerBody } from '../test/dinners.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea, hitArea } from '../test/hitArea.ts'
import styles from './Onboarding.module.css'

afterEach(() => {
  vi.unstubAllGlobals()
})

type Dinner = DinnerBody

// A request the fake server holds until the test answers it.
type Held = { method: string; path: string; body: unknown; answer: (response: Response) => void }

// stubDinnersServer stands in for the server a household meets on first
// run: signed in, onboarding not finished and the given Regulars. GET answers at once;
// each POST and DELETE is held in `held` until the test answers it, so the
// test sees the optimistic list first. Once failReads is set, every GET of
// the Regulars fails, so the test sees the list the client keeps by itself.
function stubDinnersServer(regulars: Dinner[] = []) {
  const held: Held[] = []
  const reads = { fail: false }
  const fetchMock: Mock<typeof fetch> = vi.fn<typeof fetch>((input, init) => {
    const path = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path === '/api/v1/me') return Promise.resolve(jsonResponse(200, ME))
    if (method === 'GET' && path === '/api/v1/settings/household') {
      return Promise.resolve(jsonResponse(200, { ...HOUSEHOLD_SETTINGS, onboarding_completed_at: null }))
    }
    if (method === 'GET' && path === '/api/v1/dinners?status=regular') {
      if (reads.fail) return Promise.resolve(errorResponse(500, 'internal'))
      return Promise.resolve(jsonResponse(200, { dinners: regulars }))
    }
    if ((method === 'POST' && path === '/api/v1/dinners') || (method === 'DELETE' && path.startsWith('/api/v1/dinners/'))) {
      return new Promise<Response>((resolve) => {
        held.push({ method, path, body: init?.body === undefined ? undefined : JSON.parse(String(init.body)), answer: resolve })
      })
    }
    throw new Error(`unexpected fetch: ${method} ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, held, regulars, reads }
}

// created answers a held POST /api/v1/dinners as the server does, adding the
// Regular to the list later GETs read.
function created(server: ReturnType<typeof stubDinnersServer>, request: Held, id: string) {
  const { name } = request.body as { name: string }
  server.regulars.push(dinnerBody(id, name))
  request.answer(jsonResponse(201, { ...dinnerBody(id, name, { grounding_status: 'pending' }), grounding_job_id: 'job-1' }))
}

// renderAt renders the whole app, so /me supplies the CSRF token every
// write needs and OnboardingGate reads the household settings.
function renderAt(path: string) {
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  render(
    <InDataRouter path={path}>
      <App api={api} serviceWorker={undefined} />
    </InDataRouter>,
  )
}

async function regularsStep(regulars: Dinner[] = []) {
  const server = stubDinnersServer(regulars)
  renderAt('/onboarding/regulars')
  // Common picks show once the Regulars have loaded.
  await screen.findByRole('heading', { level: 2, name: 'Common picks' })
  expect(screen.getByRole('heading', { level: 2, name: `Your regulars · ${regulars.length}` })).toBeInTheDocument()
  return server
}

function input() {
  return screen.getByRole('textbox', { name: 'Dinner name' })
}

function regularsList() {
  return screen.getByRole('heading', { level: 2, name: /^Your regulars/ }).nextElementSibling as HTMLElement
}

function regularNames() {
  return within(regularsList())
    .queryAllByRole('listitem')
    .map((item) => item.textContent)
}

function next() {
  return screen.getByRole('button', { name: 'Next: notifications' })
}

describe('onboarding step 1', () => {
  it('welcomes the household, explains the words and starts with step 2', async () => {
    stubDinnersServer()
    renderAt('/onboarding')

    expect(await screen.findByRole('heading', { level: 1, name: 'Welcome to MealQuest' })).toBeInTheDocument()
    expect(screen.getByText('Step 1 of 3')).toBeInTheDocument()
    const terms = within(screen.getByRole('list')).getAllByRole('listitem')
    expect(terms.map((term) => term.textContent)).toEqual([
      expect.stringMatching(/^Regulars/),
      expect.stringMatching(/^Side quests/),
      expect.stringMatching(/^Quest log/),
    ])
    expect(screen.queryByRole('navigation', { name: 'Primary' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: "Let's start" }))
    expect(await screen.findByRole('heading', { level: 1, name: "What's on your usual rotation?" })).toBeInTheDocument()
  })

  it('fills one progress pill of three', async () => {
    stubDinnersServer()
    renderAt('/onboarding')
    await screen.findByRole('heading', { level: 1, name: 'Welcome to MealQuest' })
    const pills = Array.from(document.getElementsByClassName(styles.pill!))
    expect(pills.map((pill) => pill.classList.contains(styles.filled!))).toEqual([true, false, false])
  })
})

describe('onboarding step 2 (mock 1h)', () => {
  it('shows the step as the mock does', async () => {
    await regularsStep([dinnerBody('d1', 'Beef tacos')])

    expect(screen.getByText('Step 2 of 3')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: "What's on your usual rotation?" })).toBeInTheDocument()
    expect(
      screen.getByText('Add the dinners your household makes on repeat. MealQuest builds suggestions from these.'),
    ).toBeInTheDocument()
    expect(input()).toHaveAttribute('placeholder', 'e.g. Chicken enchiladas')
    expect(regularNames()).toEqual(['Beef tacos'])
    expect(screen.getByRole('button', { name: 'Remove Beef tacos' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'Common picks' })).toBeInTheDocument()
    // A common pick the household already has is not offered again.
    expect(screen.queryByRole('button', { name: 'Add Beef tacos' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add Chili' })).toHaveTextContent('+Chili')

    const pills = Array.from(document.getElementsByClassName(styles.pill!))
    expect(pills.map((pill) => pill.classList.contains(styles.filled!))).toEqual([true, true, false])
    expect(declaredStyle(pills[0]!).height).toBe('6px')
  })

  it('does not offer a common pick the server would refuse as a duplicate of a Regular', async () => {
    await regularsStep([dinnerBody('d1', 'Homemade chili'), dinnerBody('d2', 'Easy Chicken Stir Fry')])

    expect(screen.queryByRole('button', { name: 'Add Chili' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add Chicken stir-fry' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add Beef tacos' })).toBeInTheDocument()
  })

  it('sizes the input pill, "+" button, picks and Next as the mock does', async () => {
    await regularsStep()
    expect(hitArea(input()).height).toBe(48)
    const add = screen.getByRole('button', { name: 'Add Regular' })
    expectMinHitArea(add)
    expect(declaredStyle(add.firstElementChild!).width).toBe('36px')
    expect(declaredStyle(add.firstElementChild!).height).toBe('36px')
    expectMinHitArea(screen.getByRole('button', { name: 'Add Chili' }))
    expect(declaredStyle(next()).height).toBe('52px')
    expect(declaredStyle(next().parentElement!).position).toBe('fixed')
  })

  it('adds a Regular on Enter, showing it before the server answers', async () => {
    const server = await regularsStep()
    fireEvent.change(input(), { target: { value: '  Chicken enchiladas ' } })
    // Enter in the input submits its form.
    fireEvent.submit(input())

    expect(input()).toHaveValue('')
    await vi.waitFor(() => expect(regularNames()).toEqual(['Chicken enchiladas']))
    expect(screen.getByRole('heading', { level: 2, name: 'Your regulars · 1' })).toBeInTheDocument()
    // Not removable until the server has given it an id.
    expect(screen.queryByRole('button', { name: 'Remove Chicken enchiladas' })).not.toBeInTheDocument()

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]).toMatchObject({ method: 'POST', path: '/api/v1/dinners', body: { name: 'Chicken enchiladas' } })
    created(server, server.held[0]!, 'd1')
    expect(await screen.findByRole('button', { name: 'Remove Chicken enchiladas' })).toBeInTheDocument()
  })

  it('adds a Regular with the "+" button', async () => {
    const server = await regularsStep()
    const add = screen.getByRole('button', { name: 'Add Regular' })
    expect(add).toBeDisabled()
    fireEvent.change(input(), { target: { value: 'Shrimp scampi' } })
    fireEvent.click(add)

    await vi.waitFor(() => expect(regularNames()).toEqual(['Shrimp scampi']))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]).toMatchObject({ method: 'POST', body: { name: 'Shrimp scampi' } })
  })

  it('adds a common pick on tap and stops offering it', async () => {
    const server = await regularsStep()
    fireEvent.click(screen.getByRole('button', { name: 'Add Chili' }))

    await vi.waitFor(() => expect(regularNames()).toEqual(['Chili']))
    expect(screen.queryByRole('button', { name: 'Add Chili' })).not.toBeInTheDocument()
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]).toMatchObject({ method: 'POST', path: '/api/v1/dinners', body: { name: 'Chili' } })
  })

  it('takes an add back out and says why when the server refuses it', async () => {
    const server = await regularsStep([dinnerBody('d1', 'Chili')])
    fireEvent.change(input(), { target: { value: 'chili' } })
    fireEvent.submit(input())
    await vi.waitFor(() => expect(regularNames()).toEqual(['Chili', 'chili']))

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add chili: duplicate_dinner message')
    expect(regularNames()).toEqual(['Chili'])
  })

  it('says why an add was refused while another add is still unanswered', async () => {
    const server = await regularsStep()
    fireEvent.click(screen.getByRole('button', { name: 'Add Burgers' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Add Chili' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    expect(regularNames()).toEqual(['Burgers', 'Chili'])

    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add Burgers: duplicate_dinner message')
    expect(regularNames()).toEqual(['Chili'])

    // The other add's answer does not hide the refusal.
    created(server, server.held[1]!, 'd1')
    await screen.findByRole('button', { name: 'Remove Chili' })
    expect(screen.getByRole('alert')).toHaveTextContent('Could not add Burgers: duplicate_dinner message')
    expect(regularNames()).toEqual(['Chili'])
  })

  it('says why each of two overlapping adds was refused', async () => {
    const server = await regularsStep()
    fireEvent.click(screen.getByRole('button', { name: 'Add Burgers' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Add Chili' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(2))

    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    server.held[1]!.answer(errorResponse(500, 'internal'))
    await vi.waitFor(() =>
      expect(screen.getAllByRole('alert').map((alert) => alert.textContent)).toEqual([
        'Could not add Burgers: duplicate_dinner message',
        'Could not add Chili: internal message',
      ]),
    )
    expect(regularNames()).toEqual([])
  })

  it('says why each of two overlapping adds of the same name was refused', async () => {
    const server = await regularsStep()
    fireEvent.change(input(), { target: { value: 'Lasagne' } })
    fireEvent.submit(input())
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    fireEvent.change(input(), { target: { value: 'Lasagne' } })
    fireEvent.submit(input())
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    expect(regularNames()).toEqual(['Lasagne', 'Lasagne'])

    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add Lasagne: duplicate_dinner message')
    expect(regularNames()).toEqual(['Lasagne'])

    // The second add's refusal shows beside the first instead of replacing it.
    server.held[1]!.answer(errorResponse(500, 'internal'))
    await vi.waitFor(() =>
      expect(screen.getAllByRole('alert').map((alert) => alert.textContent)).toEqual([
        'Could not add Lasagne: duplicate_dinner message',
        'Could not add Lasagne: internal message',
      ]),
    )
    expect(regularNames()).toEqual([])
  })

  it('removes a Regular at once and archives it on the server', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Remove Beef tacos' }))

    await vi.waitFor(() => expect(regularNames()).toEqual(['Pizza night']))
    expect(screen.getByRole('heading', { level: 2, name: 'Your regulars · 1' })).toBeInTheDocument()
    // Removed, it is a common pick again.
    expect(screen.getByRole('button', { name: 'Add Beef tacos' })).toBeInTheDocument()
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    expect(server.held[0]).toMatchObject({ method: 'DELETE', path: '/api/v1/dinners/d1' })
  })

  it('puts a removed Regular back in its place when the server refuses', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Remove Beef tacos' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(500, 'internal'))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not remove Beef tacos: internal message')
    expect(regularNames()).toEqual(['Beef tacos', 'Pizza night'])
  })

  it('puts a refused removal back and says why while another removal is still unanswered', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
      dinnerBody('d3', 'Chili'),
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Remove Beef tacos' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Remove Pizza night' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    expect(regularNames()).toEqual(['Chili'])

    server.held[0]!.answer(errorResponse(500, 'internal'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not remove Beef tacos: internal message')
    expect(regularNames()).toEqual(['Beef tacos', 'Chili'])

    // The other removal's answer does not hide the refusal.
    server.regulars.splice(1, 1)
    server.held[1]!.answer(new Response(null, { status: 204 }))
    await vi.waitFor(() => expect(server.fetchMock.mock.calls.filter(([path]) => String(path).includes('status=regular'))).toHaveLength(2))
    await vi.waitFor(() => expect(regularNames()).toEqual(['Beef tacos', 'Chili']))
    expect(screen.getByRole('alert')).toHaveTextContent('Could not remove Beef tacos: internal message')
  })

  it('puts two refused removals back in their places without rereading the list', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
      dinnerBody('d3', 'Chili'),
    ])
    server.reads.fail = true
    fireEvent.click(screen.getByRole('button', { name: 'Remove Beef tacos' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Remove Pizza night' }))
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    expect(regularNames()).toEqual(['Chili'])

    server.held[0]!.answer(errorResponse(500, 'internal'))
    await screen.findByRole('alert')
    server.held[1]!.answer(errorResponse(500, 'internal'))
    await screen.findByText('Could not remove Pizza night: internal message')
    // The reread after the last answer fails, so the order is the client's own.
    expect(await screen.findByText(/Your Regulars could not load/)).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /^Remove / }).map((button) => button.getAttribute('aria-label'))).toEqual([
      'Remove Beef tacos',
      'Remove Pizza night',
      'Remove Chili',
    ])
  })

  it('enables "Next: notifications" once the server has saved 3 Regulars', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
    ])
    expect(next()).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Add Burgers' }))
    await screen.findByRole('heading', { level: 2, name: 'Your regulars · 3' })
    // The third chip shows at once, but Next waits for the server.
    expect(next()).toBeDisabled()
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    created(server, server.held[0]!, 'd3')
    await screen.findByRole('button', { name: 'Remove Burgers' })
    expect(next()).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Remove Pizza night' }))
    await vi.waitFor(() => expect(next()).toBeDisabled())
    await vi.waitFor(() => expect(server.held).toHaveLength(2))
    server.held[1]!.answer(errorResponse(500, 'internal'))
    await vi.waitFor(() => expect(next()).toBeEnabled())

    fireEvent.click(next())
    expect(await screen.findByText('Step 3 of 3')).toBeInTheDocument()
  })

  it('keeps "Next: notifications" disabled when the third add is refused', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Add Burgers' }))
    await screen.findByRole('heading', { level: 2, name: 'Your regulars · 3' })
    expect(next()).toBeDisabled()

    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add Burgers: duplicate_dinner message')
    expect(regularNames()).toEqual(['Beef tacos', 'Pizza night'])
    expect(next()).toBeDisabled()
  })

  it('keeps "Next: notifications" disabled while a fourth add is unanswered', async () => {
    const server = await regularsStep([
      dinnerBody('d1', 'Beef tacos'),
      dinnerBody('d2', 'Pizza night'),
      dinnerBody('d3', 'Chili'),
    ])
    expect(next()).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Add Burgers' }))
    await screen.findByRole('heading', { level: 2, name: 'Your regulars · 4' })
    expect(next()).toBeDisabled()
    await vi.waitFor(() => expect(server.held).toHaveLength(1))
    server.held[0]!.answer(errorResponse(409, 'duplicate_dinner'))
    await screen.findByRole('alert')
    expect(next()).toBeEnabled()
  })
})
