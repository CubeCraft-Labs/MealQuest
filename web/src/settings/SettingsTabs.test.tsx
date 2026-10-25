import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import {
  AI_MODELS,
  AI_SETTINGS,
  CHAT_WEBHOOK,
  GOOGLE_NOT_CONNECTED,
  HOUSEHOLD_SETTINGS,
  NOTIFICATION_SETTINGS,
  RECIPE_SEARCH_SETTINGS,
  SETTINGS_REGULARS,
} from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody, type DinnerBody } from '../test/dinners.ts'
import { fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { SettingsPage } from './SettingsPage.tsx'

type Responder = (body: unknown) => Response

// stubAPI answers "METHOD path" from responders, each called with the
// request's decoded JSON body; the settings the page loads answer their
// fixtures unless overridden. Any other request fails the test.
function stubAPI(responders: Record<string, Responder> = {}): Mock<typeof fetch> {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    'GET /api/v1/settings/ai': () => jsonResponse(200, AI_SETTINGS),
    'GET /api/v1/settings/ai/models': () => jsonResponse(200, AI_MODELS),
    'GET /api/v1/settings/recipe-search': () => jsonResponse(200, RECIPE_SEARCH_SETTINGS),
    'GET /api/v1/settings/notifications': () => jsonResponse(200, NOTIFICATION_SETTINGS),
    'GET /api/v1/settings/chat-webhook': () => jsonResponse(200, CHAT_WEBHOOK),
    'GET /api/v1/dinners?status=regular': () => jsonResponse(200, SETTINGS_REGULARS),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined
    return Promise.resolve(respond(body))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

// renderAt renders /settings at width px in a data router (the page's leave
// guard needs one), after loading /me for the CSRF token writes need.
// It returns the function that resizes the viewport.
async function renderAt(width: number) {
  const resize = mockViewport(width)
  const api = createApiClient(fakeLocation('/settings'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings">
          <SettingsPage />
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
  return resize
}

const tab = (name: string) => screen.getByRole('tab', { name })
// panel is the panel a tab controls, shown or hidden.
function panel(name: string): HTMLElement {
  const id = tab(name).getAttribute('aria-controls')
  const element = id === null ? null : document.getElementById(id)
  if (element === null) throw new Error(`tab "${name}" controls no panel`)
  expect(element).toHaveAttribute('role', 'tabpanel')
  return element
}

describe.each([1440, 1180])('SettingsPage at %i px (mock 2e)', (width) => {
  it('shows the title, the shared line and the sub-tabs, Notifications & AI first', async () => {
    stubAPI()
    await renderAt(width)
    expect(screen.getByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(screen.getByText('Changes apply to everyone in the household.')).toBeInTheDocument()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    const tabs = within(screen.getByRole('tablist', { name: 'Settings sections' })).getAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Household', 'Regular dinners', 'Notifications & AI'])
    expect(tab('Notifications & AI')).toHaveAttribute('aria-selected', 'true')
    tabs.forEach(expectMinHitArea)

    const columns = panel('Notifications & AI')
    expect(columns).toBeVisible()
    const [left, right] = Array.from(columns.children)
    const notifications = within(left as HTMLElement).getByRole('region', { name: 'Notifications' })
    expect(await within(notifications).findAllByRole('switch')).toHaveLength(10)
    expect(within(notifications).getByRole('button', { name: 'Reminder time, 4:30 pm. Change' })).toBeInTheDocument()
    const chat = within(left as HTMLElement).getByRole('region', { name: 'Google Chat webhook' })
    expect(await within(chat).findByText('Connected · Dinner Crew space')).toBeInTheDocument()
    expect(within(chat).getByRole('button', { name: 'Send test' })).toBeInTheDocument()
    const ai = within(right as HTMLElement).getByRole('region', { name: 'AI assistant' })
    expect(within(ai).getByText('Admin')).toBeInTheDocument()
    expect(await within(ai).findByRole('combobox', { name: 'Provider' })).toHaveValue('anthropic')
    expect(within(ai).getByRole('textbox', { name: 'System prompt' })).toHaveValue(AI_SETTINGS.system_prompt)
    expect(within(right as HTMLElement).getByRole('region', { name: 'Recipe search' })).toBeInTheDocument()
  })

  it('switches tabs by click and by arrow keys', async () => {
    stubAPI()
    await renderAt(width)

    fireEvent.click(tab('Household'))
    expect(tab('Household')).toHaveAttribute('aria-selected', 'true')
    expect(panel('Household')).toBeVisible()
    expect(panel('Notifications & AI')).not.toBeVisible()
    expect(await within(panel('Household')).findByRole('button', { name: 'Save changes' })).toBeInTheDocument()

    fireEvent.keyDown(tab('Household'), { key: 'ArrowRight' })
    expect(tab('Regular dinners')).toHaveAttribute('aria-selected', 'true')
    expect(tab('Regular dinners')).toHaveFocus()
    expect(panel('Regular dinners')).toBeVisible()
    expect(await within(panel('Regular dinners')).findByRole('heading', { name: 'On rotation · 1' })).toBeInTheDocument()
    expect(panel('Household')).not.toBeVisible()

    fireEvent.keyDown(tab('Regular dinners'), { key: 'End' })
    expect(tab('Notifications & AI')).toHaveAttribute('aria-selected', 'true')
    fireEvent.keyDown(tab('Notifications & AI'), { key: 'ArrowRight' })
    expect(tab('Household')).toHaveAttribute('aria-selected', 'true')
    expect(tab('Household')).toHaveFocus()
  })

  it('saves the AI assistant card through the AI settings API', async () => {
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/ai': (body) => jsonResponse(200, { ...AI_SETTINGS, ...(body as object) }),
    })
    await renderAt(width)
    const ai = within(screen.getByRole('region', { name: 'AI assistant' }))
    const prompt = await ai.findByRole('textbox', { name: 'System prompt' })
    const save = ai.getByRole('button', { name: 'Save changes' })
    expect(save).toBeDisabled()

    fireEvent.change(prompt, { target: { value: 'Plan dinners for us.' } })
    fireEvent.click(save)
    expect(await ai.findByText('Changes saved.')).toBeInTheDocument()

    const writes = fetchMock.mock.calls.filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    expect(writes.map(([input, init]) => [init?.method, String(input), init?.body])).toEqual([
      ['PATCH', '/api/v1/settings/ai', JSON.stringify({ system_prompt: 'Plan dinners for us.' })],
    ])
    expect(prompt).toHaveValue('Plan dinners for us.')
    expect(save).toBeDisabled()
  })

  it('keeps an unsaved draft while another tab is shown', async () => {
    stubAPI()
    await renderAt(width)
    const prompt = await screen.findByRole('textbox', { name: 'System prompt' })
    fireEvent.change(prompt, { target: { value: 'A draft' } })

    fireEvent.click(tab('Household'))
    fireEvent.click(tab('Notifications & AI'))
    expect(screen.getByRole('textbox', { name: 'System prompt' })).toHaveValue('A draft')
  })
})

const TACOS = dinnerBody('d1', 'Beef tacos', { method: 'stovetop', minutes_total: 40, difficulty: 'easy' })
const PIZZA = dinnerBody('d3', 'Pizza night', { grounding_status: 'failed', grounding_error: 'no_recipe_found' })

// stubRegulars answers the Regulars from server.regulars, so a write's
// responder changes what the next read sees.
function stubRegulars(regulars: DinnerBody[], responders: (server: { regulars: DinnerBody[] }) => Record<string, Responder>) {
  const server = { regulars }
  const fetchMock = stubAPI({
    'GET /api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: server.regulars }),
    ...responders(server),
  })
  return { server, fetchMock }
}

// writes lists the requests other than GETs, as method, path and body.
function writes(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([input, init]) => [init?.method, String(input), init?.body])
}

// openRegulars shows the Regular dinners tab once its Regulars have loaded.
async function openRegulars() {
  fireEvent.click(tab('Regular dinners'))
  const regulars = within(panel('Regular dinners'))
  await regulars.findByRole('heading', { level: 2, name: /^On rotation/ })
  return regulars
}

function regularRow(name: string) {
  const item = within(screen.getByRole('list', { name: 'Regulars' }))
    .getAllByRole('listitem')
    .find((li) => within(li).queryByText(name) !== null)
  if (item === undefined) throw new Error(`no Regular row named ${name}`)
  return within(item)
}

describe.each([1440, 1180])('SettingsPage Regular dinners tab at %i px (mock 2e)', (width) => {
  it('lists the Regulars as /settings/regulars does, under the page title rather than its own', async () => {
    stubRegulars([TACOS, PIZZA], () => ({}))
    await renderAt(width)
    const regulars = await openRegulars()
    expect(regulars.getByRole('heading', { level: 2, name: 'On rotation · 2' })).toBeInTheDocument()
    expect(regularRow('Beef tacos').getByText('Stovetop · 40 min')).toBeInTheDocument()
    expect(regularRow('Beef tacos').getByText('Recipe found')).toBeInTheDocument()
    expect(regularRow('Pizza night').getByText('Needs a recipe')).toBeInTheDocument()
    expect(regulars.getByRole('textbox', { name: 'Add a Regular' })).toBeInTheDocument()
    expect(screen.getAllByRole('heading', { level: 1 }).map((h) => h.textContent)).toEqual(['Settings'])
    expect(regulars.queryByRole('button', { name: 'Back to Settings' })).not.toBeInTheDocument()
  })

  it('adds a Regular by name', async () => {
    const { fetchMock } = stubRegulars([TACOS], (server) => ({
      'POST /api/v1/dinners': (body) => {
        const created = dinnerBody('d9', (body as { name: string }).name, { grounding_status: 'pending' })
        server.regulars = [...server.regulars, created]
        return jsonResponse(201, { ...created, grounding_job_id: 'job-1' })
      },
    }))
    await renderAt(width)
    const regulars = await openRegulars()
    fireEvent.change(regulars.getByRole('textbox', { name: 'Add a Regular' }), { target: { value: 'Chicken enchiladas' } })
    fireEvent.click(regulars.getByRole('button', { name: 'Add Regular' }))

    expect(await regulars.findByRole('heading', { level: 2, name: 'On rotation · 2' })).toBeInTheDocument()
    expect(regularRow('Chicken enchiladas').getByText('Finding recipe…')).toBeInTheDocument()
    expect(writes(fetchMock)).toEqual([['POST', '/api/v1/dinners', JSON.stringify({ name: 'Chicken enchiladas' })]])
  })

  it('edits a Regular in its sheet', async () => {
    const { fetchMock } = stubRegulars([TACOS], (server) => ({
      'PATCH /api/v1/dinners/d1': (body) => {
        const edited = { ...TACOS, ...(body as object) }
        server.regulars = [edited]
        return jsonResponse(200, edited)
      },
    }))
    await renderAt(width)
    await openRegulars()
    fireEvent.click(regularRow('Beef tacos').getByRole('button', { name: 'Edit Beef tacos' }))
    const sheet = within(screen.getByRole('dialog', { name: 'Edit Beef tacos' }))
    fireEvent.change(sheet.getByLabelText('Minutes'), { target: { value: '25' } })
    fireEvent.click(sheet.getByRole('button', { name: 'Save changes' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(writes(fetchMock)).toEqual([['PATCH', '/api/v1/dinners/d1', JSON.stringify({ minutes_total: 25 })]])
    expect(await regularRow('Beef tacos').findByText('Stovetop · 25 min')).toBeInTheDocument()
  })

  it('fixes the recipe of a Regular that needs one', async () => {
    const { fetchMock } = stubRegulars([PIZZA], (server) => ({
      'GET /api/v1/dinners/d3/recipe-candidates': () =>
        jsonResponse(200, {
          candidates: [
            { candidate_id: 'https://example.com/pizza', title: 'Weeknight pizza', site_name: 'Example Kitchen', photo_url: null, minutes: 45 },
          ],
        }),
      'POST /api/v1/dinners/d3/ground': () => {
        const fixed = { ...PIZZA, grounding_status: 'grounded', grounding_error: null }
        server.regulars = [fixed]
        return jsonResponse(200, fixed)
      },
    }))
    await renderAt(width)
    await openRegulars()
    fireEvent.click(regularRow('Pizza night').getByRole('button', { name: 'Fix recipe for Pizza night' }))
    const sheet = within(screen.getByRole('dialog', { name: 'Fix recipe' }))
    fireEvent.click(await sheet.findByRole('button', { name: 'Use Weeknight pizza from Example Kitchen' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(writes(fetchMock)).toEqual([
      ['POST', '/api/v1/dinners/d3/ground', JSON.stringify({ candidate_id: 'https://example.com/pizza' })],
    ])
    expect(await regularRow('Pizza night').findByText('Recipe found')).toBeInTheDocument()
  })
})

describe('SettingsPage reminder time across tabs', () => {
  it('shows a reminder time saved on Notifications in the Household tab, keeping its draft', async () => {
    const saved = { ...HOUSEHOLD_SETTINGS }
    stubAPI({
      'GET /api/v1/settings/household': () => jsonResponse(200, saved),
      'PUT /api/v1/settings/notifications': (body) => {
        saved.reminder_time = (body as { reminder_time: string }).reminder_time
        return jsonResponse(200, body)
      },
    })
    await renderAt(1440)
    fireEvent.click(tab('Household'))
    const household = panel('Household')
    fireEvent.change(await within(household).findByLabelText('Dinner time'), { target: { value: '18:15' } })

    fireEvent.click(tab('Notifications & AI'))
    fireEvent.click(await screen.findByRole('button', { name: 'Reminder time, 4:30 pm. Change' }))
    const sheet = screen.getByRole('dialog', { name: 'Day-of dinner reminder' })
    fireEvent.change(within(sheet).getByLabelText('Reminder time'), { target: { value: '15:00' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    fireEvent.click(tab('Household'))
    await waitFor(() => expect(within(household).getByLabelText('Day-of reminder')).toHaveValue('15:00'))
    expect(within(household).getByLabelText('Dinner time')).toHaveValue('18:15')
  })
})

describe('SettingsPage across the 768 px breakpoint', () => {
  it('shows the mobile list below 768 px and the sub-tabs from 768 px', async () => {
    stubAPI({ 'GET /api/v1/integrations/google': () => jsonResponse(200, GOOGLE_NOT_CONNECTED) })
    const resize = await renderAt(767)
    expect(screen.getByRole('link', { name: /^AI assistant/ })).toHaveAttribute('href', '/settings/ai')
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()

    resize(768)
    expect(screen.getByRole('tablist', { name: 'Settings sections' })).toBeInTheDocument()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('keeps an unsaved draft and its leave guard across 1180 px, 390 px and back', async () => {
    stubAPI({ 'GET /api/v1/integrations/google': () => jsonResponse(200, GOOGLE_NOT_CONNECTED) })
    const resize = await renderAt(1180)
    fireEvent.change(await screen.findByRole('textbox', { name: 'System prompt' }), {
      target: { value: 'A draft' },
    })

    resize(390)
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: /^AI assistant/ }))
    const confirm = await screen.findByRole('dialog', { name: 'Leave without saving?' })
    fireEvent.click(within(confirm).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    resize(1180)
    expect(screen.getByRole('textbox', { name: 'System prompt' })).toHaveValue('A draft')
    const unload = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(unload)
    expect(unload.defaultPrevented).toBe(true)
  })
})
