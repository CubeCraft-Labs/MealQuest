import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { GROUNDING_POLL_MS } from '../dinners/api.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody, type DinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { RegularsSettingsPage } from './RegularsSettingsPage.tsx'

const REGULARS = '/api/v1/dinners?status=regular'

type Responder = (body: unknown) => Response | Promise<Response>

// stubServer stands in for the dinners API: GET of the Regulars answers
// the current server.regulars, so a test changes what the next read sees;
// every other request is answered by responders, keyed "METHOD path" and
// called with the request's decoded JSON body. Any other request fails the
// test.
function stubServer(regulars: DinnerBody[], responders: Record<string, Responder> = {}) {
  const server = { regulars, fetchMock: undefined as unknown as Mock<typeof fetch> }
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    [`GET ${REGULARS}`]: () => jsonResponse(200, { dinners: server.regulars }),
    ...responders,
  }
  server.fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined
    return Promise.resolve(respond(body))
  })
  vi.stubGlobal('fetch', server.fetchMock)
  return server
}

// sent lists the requests other than GETs, as "METHOD path" and body.
function sent(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([input, init]) => ({
      request: `${init?.method} ${String(input)}`,
      body: typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined,
    }))
}

function reads(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => (init?.method ?? 'GET') === 'GET' && String(input) === REGULARS)
    .length
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

// renderPage loads /me (for the CSRF token every write needs) and renders
// the page at /settings/regulars once its Regulars have loaded.
async function renderPage() {
  const api = createApiClient(fakeLocation('/settings/regulars'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings/regulars">
          <Routes>
            <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
  await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
}

const TACOS = dinnerBody('d1', 'Beef tacos', {
  method: 'stovetop',
  minutes_total: 40,
  difficulty: 'easy',
  is_favorite: true,
  photo_url: '/media/photos/p1-400.jpg',
})
const CHILI = dinnerBody('d2', 'Chili', { method: 'crockpot', minutes_total: 480, grounding_status: 'pending' })
const PIZZA = dinnerBody('d3', 'Pizza night', { grounding_status: 'failed', grounding_error: 'no_recipe_found' })
const ROAST = dinnerBody('d4', 'Pot roast', { method: 'oven', grounding_status: 'manual' })

function rows() {
  return within(screen.getByRole('list', { name: 'Regulars' })).queryAllByRole('listitem')
}

function row(name: string) {
  const item = rows().find((li) => within(li).queryByText(name) !== null)
  if (item === undefined) throw new Error(`no Regular row named ${name}`)
  return item
}

function addField() {
  return screen.getByRole('textbox', { name: 'Add a Regular' })
}

function add(text: string) {
  fireEvent.change(addField(), { target: { value: text } })
  fireEvent.click(screen.getByRole('button', { name: 'Add Regular' }))
}

function openRow(name: string) {
  fireEvent.click(within(row(name)).getByRole('button', { name: new RegExp(`^${name}`) }))
}

describe('RegularsSettingsPage', () => {
  it('lists each Regular with its photo, method and minutes, favorite heart and grounding badge', async () => {
    stubServer([TACOS, CHILI, PIZZA, ROAST])
    await renderPage()

    expect(screen.getByRole('heading', { level: 1, name: 'Regular dinners' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'On rotation · 4' })).toBeInTheDocument()
    expect(rows()).toHaveLength(4)

    const tacos = within(row('Beef tacos'))
    expect(tacos.getByRole('img', { name: 'Beef tacos' })).toHaveAttribute('src', '/media/photos/p1-400.jpg')
    expect(tacos.getByText('Stovetop · 40 min')).toBeInTheDocument()
    expect(tacos.getByText('Recipe found')).toBeInTheDocument()
    expect(tacos.getByRole('button', { name: 'Favorite Beef tacos' })).toHaveAttribute('aria-pressed', 'true')

    const chili = within(row('Chili'))
    expect(chili.getByText('Crock pot · 480 min')).toBeInTheDocument()
    expect(chili.getByText('Finding recipe…')).toBeInTheDocument()
    expect(chili.getByRole('button', { name: 'Favorite Chili' })).toHaveAttribute('aria-pressed', 'false')

    const pizza = within(row('Pizza night'))
    // No photo yet: the striped placeholder stands in.
    expect(pizza.getByRole('img', { name: 'Pizza night' })).toHaveTextContent('dish photo')
    expect(pizza.getByText('Needs a recipe')).toBeInTheDocument()

    expect(within(row('Pot roast')).getByText('Oven')).toBeInTheDocument()
    expect(within(row('Pot roast')).getByText('Recipe found')).toBeInTheDocument()
  })

  it('keeps every control at least 44 px', async () => {
    stubServer([TACOS])
    await renderPage()
    expectMinHitArea(screen.getByRole('button', { name: 'Add Regular' }))
    expectMinHitArea(screen.getByRole('button', { name: 'Favorite Beef tacos' }))
    expectMinHitArea(within(row('Beef tacos')).getByRole('button', { name: /^Beef tacos/ }))
    expectMinHitArea(screen.getByRole('button', { name: 'Back to Settings' }))
  })

  it('says so when there are no Regulars yet', async () => {
    stubServer([])
    await renderPage()
    expect(screen.getByRole('heading', { level: 2, name: 'On rotation · 0' })).toBeInTheDocument()
    expect(screen.getByText(/No Regulars yet/)).toBeInTheDocument()
  })

  it('reports Regulars that could not load, and retries', async () => {
    const server = stubServer([], { [`GET ${REGULARS}`]: () => errorResponse(500, 'internal') })
    const api = createApiClient(fakeLocation('/settings/regulars'), createNetworkStatus(window), createVerifiedSession(localStorage))
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/settings/regulars">
            <RegularsSettingsPage />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )
    expect(await screen.findByRole('alert')).toHaveTextContent('Your Regulars could not load: internal message')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(reads(server.fetchMock)).toBe(2))
  })

  it('adds a Regular by name, showing it at once and finding its recipe', async () => {
    const server = stubServer([TACOS], {
      'POST /api/v1/dinners': (body) => {
        const created = dinnerBody('d9', (body as { name: string }).name, { grounding_status: 'pending' })
        server.regulars = [...server.regulars, created]
        return jsonResponse(201, { ...created, grounding_job_id: 'job-1' })
      },
    })
    await renderPage()

    add('  Chicken enchiladas ')
    expect(addField()).toHaveValue('')
    expect(await screen.findByText('Chicken enchiladas')).toBeInTheDocument()
    expect(within(row('Chicken enchiladas')).getByText('Finding recipe…')).toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual([{ request: 'POST /api/v1/dinners', body: { name: 'Chicken enchiladas' } }])
    await waitFor(() => expect(row('Chicken enchiladas')).not.toHaveAttribute('aria-busy'))
    expect(screen.getByRole('heading', { level: 2, name: 'On rotation · 2' })).toBeInTheDocument()
  })

  it('adds a Regular from a pasted recipe link', async () => {
    const link = 'https://example.com/recipes/12345/beef-stew/'
    const server = stubServer([], {
      'POST /api/v1/dinners': () => {
        const created = dinnerBody('d9', 'Beef stew', { grounding_status: 'pending', recipe_url: link })
        server.regulars = [created]
        return jsonResponse(201, { ...created, grounding_job_id: 'job-1' })
      },
    })
    await renderPage()

    add(link)
    expect(await screen.findByText('Beef stew')).toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual([{ request: 'POST /api/v1/dinners', body: { recipe_url: link } }])
    expect(within(row('Beef stew')).getByText('Finding recipe…')).toBeInTheDocument()
  })

  it('takes back an add the server refuses and says why', async () => {
    stubServer([TACOS], { 'POST /api/v1/dinners': () => errorResponse(409, 'duplicate_dinner') })
    await renderPage()

    add('Beef tacos!')
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add Beef tacos!: duplicate_dinner message')
    expect(rows()).toHaveLength(1)
  })

  it('toggles the favorite heart', async () => {
    const server = stubServer([TACOS], {
      'POST /api/v1/dinners/d1/favorite': () => {
        server.regulars = [{ ...TACOS, is_favorite: false }]
        return jsonResponse(200, server.regulars[0])
      },
    })
    await renderPage()

    fireEvent.click(screen.getByRole('button', { name: 'Favorite Beef tacos' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Favorite Beef tacos' })).toHaveAttribute('aria-pressed', 'false'),
    )
    expect(sent(server.fetchMock)).toEqual([{ request: 'POST /api/v1/dinners/d1/favorite', body: undefined }])
    await waitFor(() => expect(reads(server.fetchMock)).toBe(2))
    expect(screen.getByRole('button', { name: 'Favorite Beef tacos' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('edits a Regular: name, method, minutes and difficulty', async () => {
    const server = stubServer([TACOS], {
      'PATCH /api/v1/dinners/d1': (body) => {
        const edited = { ...TACOS, ...(body as object) }
        server.regulars = [edited]
        return jsonResponse(200, edited)
      },
    })
    await renderPage()

    openRow('Beef tacos')
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    expect(sheet.getByLabelText('Name')).toHaveValue('Beef tacos')
    expect(sheet.getByLabelText('Method')).toHaveValue('stovetop')
    expect(sheet.getByLabelText('Minutes')).toHaveValue(40)
    expect(sheet.getByLabelText('Difficulty')).toHaveValue('easy')
    expect(sheet.getByRole('button', { name: 'Save changes' })).toBeDisabled()

    fireEvent.change(sheet.getByLabelText('Name'), { target: { value: 'Fish tacos' } })
    fireEvent.change(sheet.getByLabelText('Method'), { target: { value: 'grill' } })
    fireEvent.change(sheet.getByLabelText('Minutes'), { target: { value: '25' } })
    fireEvent.change(sheet.getByLabelText('Difficulty'), { target: { value: 'medium' } })
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(sent(server.fetchMock)).toEqual([
      {
        request: 'PATCH /api/v1/dinners/d1',
        body: { name: 'Fish tacos', method: 'grill', minutes_total: 25, difficulty: 'medium' },
      },
    ])
    expect(within(row('Fish tacos')).getByText('Grill · 25 min')).toBeInTheDocument()
  })

  it('sends only the changed fields, refuses bad minutes and shows a refused edit', async () => {
    const server = stubServer([TACOS], { 'PATCH /api/v1/dinners/d1': () => errorResponse(400, 'invalid_dinner') })
    await renderPage()

    openRow('Beef tacos')
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.change(sheet.getByLabelText('Minutes'), { target: { value: '0' } })
    expect(sheet.getByText('Minutes must be a whole number from 1 to 1440.')).toBeInTheDocument()
    expect(sheet.getByRole('button', { name: 'Save changes' })).toBeDisabled()

    fireEvent.change(sheet.getByLabelText('Minutes'), { target: { value: '45' } })
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))
    expect(await sheet.findByRole('alert')).toHaveTextContent('Not saved: invalid_dinner message')
    expect(sent(server.fetchMock)).toEqual([{ request: 'PATCH /api/v1/dinners/d1', body: { minutes_total: 45 } }])
  })

  it('removes a Regular only once confirmed', async () => {
    const server = stubServer([TACOS, ROAST], {
      'DELETE /api/v1/dinners/d1': () => {
        server.regulars = [ROAST]
        return jsonResponse(200, { ...TACOS, status: 'archived' })
      },
    })
    await renderPage()

    openRow('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Remove from Regulars' }))
    const confirm = within(screen.getByRole('dialog', { name: 'Remove Beef tacos?' }))
    fireEvent.click(confirm.getByRole('button', { name: 'Keep it' }))
    expect(screen.getByRole('dialog', { name: 'Edit Beef tacos' })).toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Remove from Regulars' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(within(rows()[0]!).getByText('Pot roast')).toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual([{ request: 'DELETE /api/v1/dinners/d1', body: undefined }])
  })

  it('puts a refused removal back and says why', async () => {
    stubServer([TACOS], { 'DELETE /api/v1/dinners/d1': () => errorResponse(500, 'internal') })
    await renderPage()

    openRow('Beef tacos')
    fireEvent.click(screen.getByRole('button', { name: 'Remove from Regulars' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not remove Beef tacos: internal message')
    expect(rows()).toHaveLength(1)
  })

  describe('Fix recipe', () => {
    const CANDIDATES = {
      candidates: [
        {
          candidate_id: 'https://example.com/pizza',
          title: 'Weeknight pizza',
          site_name: 'Example Kitchen',
          photo_url: '/media/photos/p7-400.jpg',
          minutes: 45,
        },
        { candidate_id: 'https://cook.test/pizza', title: 'Sheet-pan pizza', site_name: 'Cook Test', photo_url: null, minutes: null },
        { candidate_id: 'https://food.test/p', title: 'Pizza margherita', site_name: 'Food Test', photo_url: null, minutes: 30 },
      ],
    }

    function grounded(server: { regulars: DinnerBody[] }, overrides: Partial<DinnerBody>) {
      const fixed = { ...PIZZA, grounding_error: null, ...overrides }
      server.regulars = [fixed]
      return jsonResponse(200, fixed)
    }

    function fixSheet() {
      return within(screen.getByRole('dialog', { name: 'Fix recipe' }))
    }

    it('opens from a Needs a recipe row and picks one of up to three matches', async () => {
      const server = stubServer([PIZZA], {
        'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES),
        'POST /api/v1/dinners/d3/ground': () =>
          grounded(server, { grounding_status: 'grounded', photo_url: '/media/photos/p7-400.jpg', method: 'oven', minutes_total: 45 }),
      })
      await renderPage()

      openRow('Pizza night')
      const matches = within(await fixSheet().findByRole('list', { name: 'Recipe matches' }))
      const options = matches.getAllByRole('button')
      expect(options.map((option) => option.getAttribute('aria-label'))).toEqual([
        'Use Weeknight pizza from Example Kitchen',
        'Use Sheet-pan pizza from Cook Test',
        'Use Pizza margherita from Food Test',
      ])
      expect(within(options[0]!).getByRole('img', { name: 'Weeknight pizza' })).toHaveAttribute('src', '/media/photos/p7-400.jpg')
      expect(within(options[0]!).getByText('Example Kitchen')).toBeInTheDocument()
      expectMinHitArea(options[0]!)

      fireEvent.click(options[0]!)
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(sent(server.fetchMock)).toEqual([
        { request: 'POST /api/v1/dinners/d3/ground', body: { candidate_id: 'https://example.com/pizza' } },
      ])
      expect(within(row('Pizza night')).getByText('Recipe found')).toBeInTheDocument()
      expect(within(row('Pizza night')).getByText('Oven · 45 min')).toBeInTheDocument()
    })

    it('says so when no match was found', async () => {
      stubServer([PIZZA], { 'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, { candidates: [] }) })
      await renderPage()
      openRow('Pizza night')
      expect(await fixSheet().findByText('No matches were found. Paste a recipe link or type it in.')).toBeInTheDocument()
    })

    it('grounds a pasted recipe link, and says why a link was refused', async () => {
      let answers = 0
      const server = stubServer([PIZZA], {
        'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES),
        'POST /api/v1/dinners/d3/ground': () =>
          ++answers === 1 ? errorResponse(422, 'ungroundable') : grounded(server, { grounding_status: 'grounded' }),
      })
      await renderPage()

      openRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('radio', { name: 'Paste a link' }))
      fireEvent.change(fixSheet().getByLabelText('Recipe link'), { target: { value: ' https://example.com/blog/pizza ' } })
      fireEvent.click(fixSheet().getByRole('button', { name: 'Use this recipe' }))
      expect(await fixSheet().findByRole('alert')).toHaveTextContent('Not fixed: ungroundable message')

      fireEvent.change(fixSheet().getByLabelText('Recipe link'), { target: { value: 'https://example.com/pizza-2' } })
      fireEvent.click(fixSheet().getByRole('button', { name: 'Use this recipe' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(sent(server.fetchMock)).toEqual([
        { request: 'POST /api/v1/dinners/d3/ground', body: { recipe_url: 'https://example.com/blog/pizza' } },
        { request: 'POST /api/v1/dinners/d3/ground', body: { recipe_url: 'https://example.com/pizza-2' } },
      ])
      expect(within(row('Pizza night')).getByText('Recipe found')).toBeInTheDocument()
    })

    it('saves ingredients and steps typed in', async () => {
      const server = stubServer([PIZZA], {
        'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES),
        'POST /api/v1/dinners/d3/ground': () =>
          grounded(server, { grounding_status: 'manual', method: 'oven', minutes_total: 35 }),
      })
      await renderPage()

      openRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('radio', { name: 'Type it in' }))
      const save = fixSheet().getByRole('button', { name: 'Save recipe' })
      expect(save).toBeDisabled()
      fireEvent.change(fixSheet().getByLabelText('Ingredients, one per line'), {
        target: { value: '1 pizza dough\n\n 1 cup tomato sauce \n2 cups mozzarella' },
      })
      fireEvent.change(fixSheet().getByLabelText('Steps, one per line'), {
        target: { value: 'Heat the oven to 250°C.\nTop the dough and bake 12 minutes.\n' },
      })
      fireEvent.change(fixSheet().getByLabelText('Method'), { target: { value: 'oven' } })
      fireEvent.change(fixSheet().getByLabelText('Minutes'), { target: { value: '35' } })
      fireEvent.click(save)

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(sent(server.fetchMock)).toEqual([
        {
          request: 'POST /api/v1/dinners/d3/ground',
          body: {
            manual: {
              ingredients: ['1 pizza dough', '1 cup tomato sauce', '2 cups mozzarella'],
              steps: ['Heat the oven to 250°C.', 'Top the dough and bake 12 minutes.'],
              minutes: 35,
              method: 'oven',
            },
          },
        },
      ])
      expect(within(row('Pizza night')).getByText('Recipe found')).toBeInTheDocument()
      expect(within(row('Pizza night')).getByText('Oven · 35 min')).toBeInTheDocument()
    })

    it('switches to editing the Regular', async () => {
      stubServer([PIZZA], { 'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES) })
      await renderPage()
      openRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('button', { name: 'Edit or remove Pizza night' }))
      expect(screen.getByRole('dialog', { name: 'Edit Pizza night' })).toBeInTheDocument()
    })
  })

  it('refreshes grounding status while any Regular is finding its recipe, then stops', async () => {
    // Only intervals are faked: TanStack Query polls with setInterval, and
    // Testing Library's waits keep their real timers.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const server = stubServer([TACOS, CHILI])
    await renderPage()
    expect(within(row('Chili')).getByText('Finding recipe…')).toBeInTheDocument()
    expect(reads(server.fetchMock)).toBe(1)

    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS))
    await waitFor(() => expect(reads(server.fetchMock)).toBe(2))
    expect(within(row('Chili')).getByText('Finding recipe…')).toBeInTheDocument()

    server.regulars = [TACOS, { ...CHILI, grounding_status: 'grounded', photo_url: '/media/photos/p2-400.jpg' }]
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS))
    expect(await within(row('Chili')).findByText('Recipe found')).toBeInTheDocument()
    expect(within(row('Chili')).getByRole('img', { name: 'Chili' })).toHaveAttribute('src', '/media/photos/p2-400.jpg')
    expect(reads(server.fetchMock)).toBe(3)

    // Nothing is pending any more, so nothing is read again.
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * 3))
    expect(reads(server.fetchMock)).toBe(3)
  })

  it('does not poll when no Regular is finding its recipe', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const server = stubServer([TACOS, PIZZA])
    await renderPage()
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * 3))
    expect(reads(server.fetchMock)).toBe(1)
  })
})
