import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { GROUNDING_POLL_MS } from '../dinners/api.ts'
import { DinnerDetailPage } from '../dinners/DinnerDetailPage.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import tokens from '../styles/tokens.css?raw'
import { declaredStyle } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody, type DinnerBody } from '../test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { RegularsSettingsPage } from './RegularsSettingsPage.tsx'

const REGULARS = '/api/v1/dinners?status=regular'

// token is a design token's value as tokens.css declares it, given the
// var() that names it.
function token(reference: string): string {
  const name = /^var\((--[\w-]+)\)$/.exec(reference)?.[1]
  const value = name && new RegExp(`${name}:\\s*([^;\\s]+);`).exec(tokens)?.[1]
  if (!value) throw new Error(`${reference} is not a token in tokens.css`)
  return value
}

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
  Reflect.deleteProperty(window, 'matchMedia')
})

// Where shows the path the router is at.
function Where() {
  return <output aria-label="Path">{useLocation().pathname}</output>
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the page at /settings/regulars once its Regulars have loaded, routed
// beside the dinner detail screen a row opens.
async function renderPage() {
  const api = createApiClient(fakeLocation('/settings/regulars'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings/regulars">
          <Routes>
            <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
            <Route path="/dinner/:id" element={<DinnerDetailPage />} />
          </Routes>
          <Where />
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
const PIZZA = dinnerBody('d3', 'Pizza night', {
  grounding_status: 'failed',
  grounding_error: 'no_recipe_found',
  photo_backfill_eligible: true,
})
const ROAST = dinnerBody('d4', 'Pot roast', { method: 'oven', grounding_status: 'manual' })

// TACOS_RECIPE is Beef tacos as GET /api/v1/dinners/{id} sends it, with its
// ingredients and steps.
const TACOS_RECIPE = {
  id: 'd1',
  name: 'Beef tacos',
  status: 'regular',
  origin: 'regular',
  method: 'stovetop',
  minutes_total: 40,
  difficulty: 'easy',
  cuisine: null,
  is_favorite: true,
  photo_urls: null,
  ingredients: [
    { name: 'Ground beef', qty: '1', unit: 'lb', note: null, raw_text: '1 lb ground beef' },
    { name: 'Corn tortillas', qty: '12', unit: null, note: null, raw_text: '12 corn tortillas' },
  ],
  steps: ['Brown the beef.', 'Warm the tortillas and fill them.'],
  source: { site_name: 'Example Kitchen' },
  grounding_status: 'grounded',
  grounding_error: null,
  why_picked: null,
}

function path() {
  return screen.getByRole('status', { name: 'Path' })
}

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

// openRow taps the Regular's row itself, which opens its recipe.
function openRow(name: string) {
  fireEvent.click(within(row(name)).getByRole('button', { name: new RegExp(`^${name}`) }))
}

// editRow taps the Regular's pencil, which opens its edit sheet.
function editRow(name: string) {
  fireEvent.click(within(row(name)).getByRole('button', { name: `Edit ${name}` }))
}

// fixRow taps the pencil of a Regular whose recipe was not found, which
// opens its fix-recipe sheet.
function fixRow(name: string) {
  fireEvent.click(within(row(name)).getByRole('button', { name: `Fix recipe for ${name}` }))
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
    expectMinHitArea(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    expectMinHitArea(screen.getByRole('button', { name: 'Back to Settings' }))
  })

  it("keeps the pencil's and the heart's hit boxes apart at mobile width", async () => {
    mockViewport(390)
    stubServer([TACOS])
    await renderPage()
    const pencil = screen.getByRole('button', { name: 'Edit Beef tacos' })
    const heart = screen.getByRole('button', { name: 'Favorite Beef tacos' })
    // Side by side in one flex row: nothing else between them.
    expect(pencil.nextElementSibling).toBe(heart)
    const actions = pencil.parentElement!
    expect(declaredStyle(actions).display).toBe('inline-flex')
    const gap = declaredStyle(actions).getPropertyValue('gap')
    expect(gap).toMatch(/^var\(--space-\d\)$/)

    // jsdom does no layout, so lay the two out from their declared styles:
    // each button's border box is its hit box; margins place it in the row.
    const length = (value: string) => {
      const match = /^(-?\d+(?:\.\d+)?)px$/.exec(value)
      if (!match) throw new Error(`${JSON.stringify(value)} is not a px length`)
      return Number(match[1])
    }
    const [pencilStyle, heartStyle] = [declaredStyle(pencil), declaredStyle(heart)]
    const pencilHitEnd = length(pencilStyle.marginLeft) + length(pencilStyle.width)
    const heartHitStart =
      length(pencilStyle.marginLeft) + length(pencilStyle.width) + length(pencilStyle.marginRight) + length(token(gap)) + length(heartStyle.marginLeft)
    expect(heartHitStart - pencilHitEnd, 'space between the hit boxes').toBeGreaterThan(0)
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

  it('adds a Regular from a pasted recipe link under the name the household gives it', async () => {
    const link = 'https://example.com/recipes/stew-key-abcdefghijklmno'
    const server = stubServer([], {
      'POST /api/v1/dinners': (body) => {
        const created = dinnerBody('d9', (body as { name: string }).name, { grounding_status: 'pending' })
        server.regulars = [created]
        return jsonResponse(201, { ...created, grounding_job_id: 'job-1' })
      },
    })
    await renderPage()

    fireEvent.change(addField(), { target: { value: ` ${link} ` } })
    // A link alone cannot be added: it never names a dinner.
    expect(screen.getByRole('button', { name: 'Add Regular' })).toBeDisabled()
    const name = screen.getByRole('textbox', { name: 'Dinner name' })
    expect(name).toHaveAccessibleDescription(/never names a dinner/)
    fireEvent.change(name, { target: { value: ' Sunday stew ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add Regular' }))

    expect(addField()).toHaveValue('')
    expect(screen.queryByRole('textbox', { name: 'Dinner name' })).not.toBeInTheDocument()
    expect(await screen.findByText('Sunday stew')).toBeInTheDocument()
    expect(sent(server.fetchMock)).toEqual([
      { request: 'POST /api/v1/dinners', body: { name: 'Sunday stew', recipe_url: link } },
    ])
    expect(within(row('Sunday stew')).getByText('Finding recipe…')).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/example\.com|abcdefghijklmno|key/i)
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

  it("opens a Regular's recipe, its ingredients and steps, from its row", async () => {
    // The detail screen lays itself out by the viewport: a phone's.
    mockViewport(390)
    stubServer([TACOS], { 'GET /api/v1/dinners/d1': () => jsonResponse(200, TACOS_RECIPE) })
    await renderPage()

    openRow('Beef tacos')
    expect(path()).toHaveTextContent('/dinner/d1')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(await screen.findByRole('heading', { level: 2, name: 'Ingredients' })).toBeInTheDocument()
    const [ingredients, steps] = screen.getAllByRole('list')
    expect(within(ingredients!).getAllByRole('listitem').map((item) => item.firstElementChild!.textContent)).toEqual([
      'Ground beef',
      'Corn tortillas',
    ])
    expect(screen.getByRole('heading', { level: 2, name: 'Steps' })).toBeInTheDocument()
    expect(within(steps!).getAllByRole('listitem').map((step) => step.textContent)).toEqual([
      '1Brown the beef.',
      '2Warm the tortillas and fill them.',
    ])
  })

  it('opens a Regular whose recipe was not found at its recipe too, and its pencil fixes the recipe', async () => {
    stubServer([PIZZA], { 'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, { candidates: [] }) })
    await renderPage()

    fixRow('Pizza night')
    expect(screen.getByRole('dialog', { name: 'Fix recipe' })).toBeInTheDocument()
    expect(path()).toHaveTextContent('/settings/regulars')
    expect(within(row('Pizza night')).queryByRole('button', { name: 'Edit Pizza night' })).not.toBeInTheDocument()
  })

  it('opens the detail of a Regular whose recipe was not found from its row, saying there is no recipe yet', async () => {
    mockViewport(390)
    stubServer([PIZZA], {
      'GET /api/v1/dinners/d3': () =>
        jsonResponse(200, {
          ...TACOS_RECIPE,
          id: 'd3',
          name: 'Pizza night',
          method: null,
          minutes_total: null,
          difficulty: null,
          is_favorite: false,
          ingredients: [],
          steps: [],
          source: null,
          grounding_status: 'failed',
          grounding_error: 'no_recipe_found',
        }),
    })
    await renderPage()

    openRow('Pizza night')
    expect(path()).toHaveTextContent('/dinner/d3')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    const missing = await screen.findByRole('region', { name: 'No recipe' })
    expect(missing).toHaveTextContent("We couldn't find a recipe for this dinner, so there are no ingredients or steps yet.")
    expect(within(missing).getByRole('link', { name: 'Fix recipe' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 2, name: 'Ingredients' })).not.toBeInTheDocument()
  })

  it('keeps the pencil, not the row, opening the edit sheet, and neither while it is being added', async () => {
    const answer = Promise.withResolvers<Response>()
    stubServer([TACOS], { 'POST /api/v1/dinners': () => answer.promise })
    await renderPage()

    editRow('Beef tacos')
    expect(screen.getByRole('dialog', { name: 'Edit Beef tacos' })).toBeInTheDocument()
    expect(path()).toHaveTextContent('/settings/regulars')

    add('Chicken enchiladas')
    const adding = within(await waitFor(() => row('Chicken enchiladas')))
    expect(adding.getByRole('button', { name: 'Edit Chicken enchiladas' })).toBeDisabled()
    expect(adding.queryByRole('button', { name: /^Chicken enchiladas/ })).not.toBeInTheDocument()
    answer.resolve(errorResponse(500, 'internal_error'))
    await waitFor(() => expect(screen.queryByText('Chicken enchiladas')).not.toBeInTheDocument())
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

    editRow('Beef tacos')
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

    editRow('Beef tacos')
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

    editRow('Beef tacos')
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

    editRow('Beef tacos')
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

      fixRow('Pizza night')
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
      fixRow('Pizza night')
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

      fixRow('Pizza night')
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

      fixRow('Pizza night')
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

    it('saves a typed-in recipe only once its method and minutes are set', async () => {
      const server = stubServer([PIZZA], {
        'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES),
      })
      await renderPage()

      fixRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('radio', { name: 'Type it in' }))
      const save = fixSheet().getByRole('button', { name: 'Save recipe' })
      fireEvent.change(fixSheet().getByLabelText('Ingredients, one per line'), { target: { value: '1 pizza dough' } })
      expect(fixSheet().getByText('Choose a method and minutes to save.')).toBeInTheDocument()
      expect(save).toBeDisabled()

      fireEvent.change(fixSheet().getByLabelText('Method'), { target: { value: 'oven' } })
      expect(save).toBeDisabled()
      fireEvent.change(fixSheet().getByLabelText('Method'), { target: { value: '' } })
      fireEvent.change(fixSheet().getByLabelText('Minutes'), { target: { value: '35' } })
      expect(save).toBeDisabled()

      // Even a submit that skips the button sends nothing.
      fireEvent.submit(save.closest('form')!)
      expect(sent(server.fetchMock)).toEqual([])

      fireEvent.change(fixSheet().getByLabelText('Method'), { target: { value: 'oven' } })
      expect(fixSheet().queryByText('Choose a method and minutes to save.')).not.toBeInTheDocument()
      expect(save).toBeEnabled()
    })

    it('switches to editing the Regular', async () => {
      stubServer([PIZZA], { 'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, CANDIDATES) })
      await renderPage()
      fixRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('button', { name: 'Edit or remove Pizza night' }))
      expect(screen.getByRole('dialog', { name: 'Edit Pizza night' })).toBeInTheDocument()
    })
  })

  describe('Photo backfill', () => {
    const BACKFILL = 'POST /api/v1/settings/photo-backfill'
    const JOB = 'GET /api/v1/jobs/j9'

    // backfillJob is a GET /api/v1/jobs/j9 answer for the photo backfill.
    function backfillJob(status: string, result: unknown = null, error: string | null = null) {
      return jsonResponse(200, { id: 'j9', kind: 'backfill_dinner_photos', status, result, error })
    }

    // polls answers each poll of the job with the next answer, then the last
    // one again; an answer may change the server first.
    function polls(...answers: (() => Response)[]) {
      let next = 0
      return () => answers[Math.min(next++, answers.length - 1)]!()
    }

    function fixSheet() {
      return within(screen.getByRole('dialog', { name: 'Fix recipe' }))
    }

    const NO_CANDIDATES = { 'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, { candidates: [] }) }

    it('shows why the recipe was not found in the fix-recipe sheet', async () => {
      stubServer([PIZZA], NO_CANDIDATES)
      await renderPage()

      fixRow('Pizza night')
      expect(fixSheet().getByText('No recipe page the search found had a recipe we could use.')).toBeInTheDocument()
      expect(fixSheet().getByRole('button', { name: 'Search again' })).toBeEnabled()
    })

    it('offers no search again for a pasted link that had no usable recipe', async () => {
      const linked = { ...PIZZA, grounding_error: 'recipe_unusable', photo_backfill_eligible: false }
      stubServer([linked], NO_CANDIDATES)
      await renderPage()

      fixRow('Pizza night')
      expect(fixSheet().getByText('The pasted recipe link has no recipe we could use.')).toBeInTheDocument()
      expect(fixSheet().queryByRole('button', { name: 'Search again' })).not.toBeInTheDocument()
    })

    it('offers no search again for a dinner with a pasted link whatever its reason', async () => {
      const linked = { ...PIZZA, grounding_error: 'ai_failed', photo_backfill_eligible: false }
      stubServer([linked], NO_CANDIDATES)
      await renderPage()

      fixRow('Pizza night')
      expect(fixSheet().getByText("The AI assistant couldn't read the recipe.")).toBeInTheDocument()
      expect(fixSheet().queryByRole('button', { name: 'Search again' })).not.toBeInTheDocument()
    })

    it('searches again from the fix-recipe sheet and shows the new reason when it still fails', async () => {
      const server = stubServer([PIZZA], {
        ...NO_CANDIDATES,
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: polls(
          () => backfillJob('running'),
          () => {
            server.regulars = [{ ...PIZZA, grounding_error: 'search_failed' }]
            return backfillJob('succeeded', { dinners: [{ dinner_id: 'd3', outcome: 'failed', reason: 'search_failed' }] })
          },
        ),
      })
      await renderPage()

      fixRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('button', { name: 'Search again' }))
      expect(await fixSheet().findByRole('button', { name: 'Searching again…' })).toBeDisabled()
      expect(fixSheet().getByText('Searching again… this can take a few minutes.')).toBeInTheDocument()
      expect(sent(server.fetchMock)).toEqual([{ request: BACKFILL, body: undefined }])

      expect(await fixSheet().findByText('Searched again: still no recipe found.', {}, { timeout: 3000 })).toBeInTheDocument()
      expect(await fixSheet().findByText("The recipe search couldn't be reached.")).toBeInTheDocument()
      expect(fixSheet().getByRole('button', { name: 'Search again' })).toBeEnabled()
    })

    it('closes the fix-recipe sheet once searching again finds the recipe and its photo', async () => {
      const server = stubServer([PIZZA], {
        ...NO_CANDIDATES,
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: () => {
          server.regulars = [{ ...PIZZA, grounding_status: 'grounded', grounding_error: null, photo_url: '/media/photos/p8-400.jpg' }]
          return backfillJob('succeeded', { dinners: [{ dinner_id: 'd3', outcome: 'grounded', reason: null }] })
        },
      })
      await renderPage()

      fixRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('button', { name: 'Search again' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(within(row('Pizza night')).getByText('Recipe found')).toBeInTheDocument()
      expect(within(row('Pizza night')).getByRole('img', { name: 'Pizza night' })).toHaveAttribute('src', '/media/photos/p8-400.jpg')
    })

    it('says when a dinner was not included in the search', async () => {
      stubServer([PIZZA], {
        ...NO_CANDIDATES,
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: () => backfillJob('succeeded', { dinners: [] }),
      })
      await renderPage()

      fixRow('Pizza night')
      fireEvent.click(fixSheet().getByRole('button', { name: 'Search again' }))
      expect(await fixSheet().findByText('This dinner was not included in that search.')).toBeInTheDocument()
    })

    it('finds missing photos: the placeholder shows until the backfill finds a photo', async () => {
      const found = { ...PIZZA, grounding_status: 'grounded', grounding_error: null, photo_url: '/media/photos/p8-400.jpg' }
      const server = stubServer([TACOS, PIZZA], {
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: polls(
          () => backfillJob('queued'),
          () => {
            server.regulars = [TACOS, found]
            return backfillJob('succeeded', { dinners: [{ dinner_id: 'd3', outcome: 'grounded', reason: null }] })
          },
        ),
      })
      await renderPage()

      // The photoless dinner shows the striped placeholder, not an image.
      const placeholder = within(row('Pizza night')).getByRole('img', { name: 'Pizza night' })
      expect(placeholder).not.toHaveAttribute('src')
      expect(placeholder).toHaveTextContent('dish photo')

      const trigger = screen.getByRole('button', { name: 'Find missing photos' })
      expectMinHitArea(trigger)
      fireEvent.click(trigger)
      expect(await screen.findByRole('button', { name: 'Finding photos…' })).toBeDisabled()
      expect(await screen.findByText('Finding missing photos… this can take a few minutes.')).toBeInTheDocument()
      expect(within(row('Pizza night')).getByRole('img', { name: 'Pizza night' })).not.toHaveAttribute('src')
      expect(sent(server.fetchMock)).toEqual([{ request: BACKFILL, body: undefined }])

      expect(await screen.findByText('Photo search done: 1 photo found.', {}, { timeout: 3000 })).toBeInTheDocument()
      await waitFor(() =>
        expect(within(row('Pizza night')).getByRole('img', { name: 'Pizza night' })).toHaveAttribute('src', '/media/photos/p8-400.jpg'),
      )
      expect(screen.getByRole('button', { name: 'Find missing photos' })).toBeEnabled()
      expect(reads(server.fetchMock)).toBe(2)
    })

    it('sums up what the backfill did', async () => {
      stubServer([PIZZA], {
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: () =>
          backfillJob('succeeded', {
            dinners: [
              { dinner_id: 'd3', outcome: 'grounded', reason: null },
              { dinner_id: 'd5', outcome: 'grounded', reason: null },
              { dinner_id: 'd6', outcome: 'failed', reason: 'no_recipe_found' },
              { dinner_id: 'd7', outcome: 'skipped', reason: null },
            ],
          }),
      })
      await renderPage()

      fireEvent.click(screen.getByRole('button', { name: 'Find missing photos' }))
      expect(
        await screen.findByText(
          'Photo search done: 2 photos found · 1 dinner still needs a recipe · 1 dinner changed meanwhile, left as it was.',
        ),
      ).toBeInTheDocument()
    })

    it('says when no dinner was missing a photo', async () => {
      stubServer([TACOS], {
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: () => backfillJob('succeeded', { dinners: [] }),
      })
      await renderPage()

      fireEvent.click(screen.getByRole('button', { name: 'Find missing photos' }))
      expect(await screen.findByText('No dinners were missing a photo.')).toBeInTheDocument()
    })

    it('says why the backfill stopped', async () => {
      stubServer([PIZZA], {
        [BACKFILL]: () => jsonResponse(202, { id: 'j9' }),
        [JOB]: () => backfillJob('failed', { dinners: [] }, 'internal_error'),
      })
      await renderPage()

      fireEvent.click(screen.getByRole('button', { name: 'Find missing photos' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('Photo search stopped: Something went wrong on the server.')
      expect(screen.getByRole('button', { name: 'Find missing photos' })).toBeEnabled()
    })

    it('says why the backfill could not start', async () => {
      stubServer([PIZZA], { [BACKFILL]: () => errorResponse(403, 'csrf_failed') })
      await renderPage()

      fireEvent.click(screen.getByRole('button', { name: 'Find missing photos' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('Photo search not started: csrf_failed message')
      expect(screen.getByRole('button', { name: 'Find missing photos' })).toBeEnabled()
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

  // held answers a request only when release is called, so a test can look
  // at the page while a save is in flight.
  function held(answer: () => Response) {
    let release = () => {}
    const response = new Promise<Response>((resolve) => {
      release = () => resolve(answer())
    })
    return { respond: () => response, release: () => release() }
  }

  it('pauses the refresh while an edit is being saved, then resumes', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const edited = { ...TACOS, name: 'Fish tacos' }
    const patch = held(() => jsonResponse(200, edited))
    const server = stubServer([TACOS, CHILI], { 'PATCH /api/v1/dinners/d1': patch.respond })
    await renderPage()

    editRow('Beef tacos')
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.change(sheet.getByLabelText('Name'), { target: { value: 'Fish tacos' } })
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(sent(server.fetchMock)).toHaveLength(1))

    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * 3))
    expect(reads(server.fetchMock)).toBe(1)

    server.regulars = [edited, CHILI]
    patch.release()
    // Settling reads the list once, then the refresh carries on.
    await waitFor(() => expect(reads(server.fetchMock)).toBe(2))
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS))
    await waitFor(() => expect(reads(server.fetchMock)).toBe(3))
  })

  it('pauses the refresh while a recipe fix is being saved, then resumes', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const fixed = { ...PIZZA, grounding_status: 'grounded' as const, grounding_error: null }
    const ground = held(() => jsonResponse(200, fixed))
    const server = stubServer([CHILI, PIZZA], {
      'GET /api/v1/dinners/d3/recipe-candidates': () => jsonResponse(200, { candidates: [] }),
      'POST /api/v1/dinners/d3/ground': ground.respond,
    })
    await renderPage()

    fixRow('Pizza night')
    const sheet = within(screen.getByRole('dialog', { name: 'Fix recipe' }))
    fireEvent.click(sheet.getByRole('radio', { name: 'Paste a link' }))
    fireEvent.change(sheet.getByLabelText('Recipe link'), { target: { value: 'https://example.com/pizza' } })
    fireEvent.click(sheet.getByRole('button', { name: 'Use this recipe' }))
    await waitFor(() => expect(sent(server.fetchMock)).toHaveLength(1))

    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * 3))
    expect(reads(server.fetchMock)).toBe(1)

    server.regulars = [CHILI, fixed]
    ground.release()
    await waitFor(() => expect(reads(server.fetchMock)).toBe(2))
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS))
    await waitFor(() => expect(reads(server.fetchMock)).toBe(3))
  })

  it('does not poll when no Regular is finding its recipe', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const server = stubServer([TACOS, PIZZA])
    await renderPage()
    await act(() => vi.advanceTimersByTimeAsync(GROUNDING_POLL_MS * 3))
    expect(reads(server.fetchMock)).toBe(1)
  })
})
