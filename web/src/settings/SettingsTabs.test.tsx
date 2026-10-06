import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { AI_MODELS, AI_SETTINGS, HOUSEHOLD_SETTINGS, RECIPE_SEARCH_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
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
    expect(within(left as HTMLElement).getByRole('region', { name: 'Notifications & Google Chat' })).toBeInTheDocument()
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
    expect(within(panel('Regular dinners')).getByRole('region', { name: 'Regular dinners' })).toBeInTheDocument()
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

describe('SettingsPage across the 768 px breakpoint', () => {
  it('shows the mobile list below 768 px and the sub-tabs from 768 px', async () => {
    stubAPI({
      'GET /api/v1/dinners?status=regular': () => errorResponse(404, 'not_found'),
      'GET /api/v1/settings/calendar': () => errorResponse(404, 'not_found'),
      'GET /api/v1/settings/notifications': () => errorResponse(404, 'not_found'),
    })
    const resize = await renderAt(767)
    expect(screen.getByRole('link', { name: /^AI assistant/ })).toHaveAttribute('href', '/settings/ai')
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()

    resize(768)
    expect(screen.getByRole('tablist', { name: 'Settings sections' })).toBeInTheDocument()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('keeps an unsaved draft and its leave guard across 1180 px, 390 px and back', async () => {
    stubAPI({
      'GET /api/v1/dinners?status=regular': () => errorResponse(404, 'not_found'),
      'GET /api/v1/settings/calendar': () => errorResponse(404, 'not_found'),
      'GET /api/v1/settings/notifications': () => errorResponse(404, 'not_found'),
    })
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
