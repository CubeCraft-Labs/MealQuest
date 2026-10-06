import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { AI_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { AISettingsPage, formatVerifiedDate, maskedKey } from './AISettingsPage.tsx'

const MODELS = {
  models: [
    { id: 'claude-sonnet-4-5', display_name: 'Claude Sonnet' },
    { id: 'claude-haiku-4-5', display_name: 'Claude Haiku' },
  ],
}

const DEFAULT_PROMPT = 'You are MealQuest, the default persona.'

type Responder = (body: unknown) => Response

// stubAPI answers "METHOD path" from responders, each called with the
// request's decoded JSON body; GET /settings/ai and /settings/ai/models
// answer AI_SETTINGS and MODELS unless overridden. Any other request fails
// the test.
function stubAPI(responders: Record<string, Responder> = {}): Mock<typeof fetch> {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/ai': () => jsonResponse(200, AI_SETTINGS),
    'GET /api/v1/settings/ai/models': () => jsonResponse(200, MODELS),
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

// sent lists the requests other than GETs, as "METHOD path" and body.
function sent(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? 'GET') !== 'GET')
    .map(([input, init]) => ({
      request: `${init?.method} ${String(input)}`,
      body: typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined,
    }))
}

afterEach(() => {
  vi.unstubAllGlobals()
})

function CurrentPath() {
  return <p data-testid="path">{useLocation().pathname}</p>
}

// renderPage loads /me (for the CSRF token every write needs) and renders
// the page at /settings/ai in a data router, so its leave guard works.
async function renderPage() {
  const api = createApiClient(fakeLocation('/settings/ai'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path="/settings/ai">
          <Routes>
            <Route path="/settings/ai" element={<AISettingsPage />} />
            <Route path="*" element={<CurrentPath />} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
  await screen.findByRole('option', { name: 'Claude Sonnet' })
}

const provider = () => screen.getByRole('combobox', { name: 'Provider' })
const model = () => screen.getByRole('combobox', { name: 'Model' })
const prompt = () => screen.getByRole('textbox', { name: 'System prompt' })
const slider = () => screen.getByRole('slider', { name: 'Creativity (temperature)' })
const save = () => screen.getByRole('button', { name: 'Save changes' })

describe('AISettingsPage', () => {
  it('loads the settings as mock 1o lays them out', async () => {
    stubAPI()
    await renderPage()
    expect(screen.getByRole('button', { name: 'Back to Settings' })).toBeInTheDocument()
    expect(screen.getByText('Settings · Admin')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'AI assistant' })).toBeInTheDocument()
    expect(provider()).toHaveValue('anthropic')
    expect(within(provider()).getByRole('option', { selected: true })).toHaveTextContent('Anthropic')
    expect(model()).toHaveValue('claude-sonnet-4-5')
    expect(within(model()).getAllByRole('option').map((o) => o.textContent)).toEqual([
      'Claude Sonnet',
      'Claude Haiku',
      'Type a model id',
    ])
    expect(screen.getByText('••••••••3f9a')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Replace' })).toBeInTheDocument()
    expect(screen.getByText('Verified · Oct 1')).toBeInTheDocument()
    expect(slider()).toHaveValue('0.7')
    expect(slider()).toHaveAttribute('step', '0.1')
    expect(screen.getByText('0.7')).toBeInTheDocument()
    expect(screen.getByText('Familiar')).toBeInTheDocument()
    expect(screen.getByText('Adventurous')).toBeInTheDocument()
    expect(prompt()).toHaveValue(AI_SETTINGS.system_prompt)
    expect(screen.getByRole('button', { name: 'Reset' })).toBeEnabled()
    expect(save()).toBeDisabled()
  })

  it('reports a load failure with a retry', async () => {
    stubAPI({ 'GET /api/v1/settings/ai': () => errorResponse(500, 'internal') })
    const api = createApiClient(fakeLocation('/settings/ai'), createNetworkStatus(window), createVerifiedSession(localStorage))
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/settings/ai">
            <AISettingsPage />
          </InDataRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )
    expect(await screen.findByRole('alert')).toHaveTextContent('AI settings could not load: internal message')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('saves only the edited fields', async () => {
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/ai': (body) => jsonResponse(200, { ...AI_SETTINGS, ...(body as object) }),
    })
    await renderPage()
    fireEvent.change(model(), { target: { value: 'claude-haiku-4-5' } })
    fireEvent.change(slider(), { target: { value: '0.4' } })
    expect(screen.getByText('0.4')).toBeInTheDocument()
    fireEvent.change(prompt(), { target: { value: 'Keep it cozy.' } })
    expect(save()).toBeEnabled()
    fireEvent.click(save())

    expect(await screen.findByText('Changes saved.')).toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([
      {
        request: 'PATCH /api/v1/settings/ai',
        body: { model: 'claude-haiku-4-5', temperature: 0.4, system_prompt: 'Keep it cozy.' },
      },
    ])
    expect(save()).toBeDisabled()
    expect(model()).toHaveValue('claude-haiku-4-5')
  })

  it('saves a typed model id', async () => {
    const fetchMock = stubAPI({
      'PATCH /api/v1/settings/ai': (body) => jsonResponse(200, { ...AI_SETTINGS, ...(body as object) }),
    })
    await renderPage()
    expect(screen.queryByRole('textbox', { name: 'Model id' })).not.toBeInTheDocument()
    fireEvent.change(model(), { target: { value: within(model()).getByRole('option', { name: 'Type a model id' }).getAttribute('value') } })
    fireEvent.change(screen.getByRole('textbox', { name: 'Model id' }), { target: { value: 'claude-opus-5-5' } })
    fireEvent.click(save())
    await screen.findByText('Changes saved.')
    expect(sent(fetchMock)).toEqual([{ request: 'PATCH /api/v1/settings/ai', body: { model: 'claude-opus-5-5' } }])
  })

  it('explains that a stored key must be removed before switching provider', async () => {
    stubAPI({
      'PATCH /api/v1/settings/ai': () =>
        jsonResponse(400, {
          error: {
            code: 'invalid_settings',
            message: 'invalid AI settings: provider: cannot change while an API key is stored: remove the key first',
            retryable: false,
          },
        }),
    })
    await renderPage()
    fireEvent.change(provider(), { target: { value: 'openai' } })
    expect(screen.getByText(/Anthropic's API key is stored\. Remove it/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Replace' })).toBeDisabled()
    fireEvent.change(screen.getByRole('textbox', { name: 'Model id' }), { target: { value: 'gpt-5' } })
    fireEvent.click(save())
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Not saved: invalid AI settings: provider: cannot change while an API key is stored')
    expect(alert).toHaveTextContent('To switch provider, remove the API key first')
  })

  it('replaces the key in a sheet, then verifies it', async () => {
    const verifiedAt = '2026-10-06T14:00:00.000Z'
    const fetchMock = stubAPI({
      'PUT /api/v1/settings/ai/api-key': () =>
        jsonResponse(200, { ...AI_SETTINGS, api_key_last4: 'b7c1', key_verified_at: null }),
      'POST /api/v1/settings/ai/verify': () =>
        jsonResponse(200, { ...AI_SETTINGS, api_key_last4: 'b7c1', key_verified_at: verifiedAt }),
    })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }))
    const sheet = screen.getByRole('dialog', { name: 'Replace API key' })
    const input = within(sheet).getByLabelText('API key')
    expect(input).toHaveAttribute('type', 'password')
    fireEvent.change(input, { target: { value: 'sk-ant-secret-b7c1' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save and verify' }))

    expect(await screen.findByText(`Verified · ${formatVerifiedDate(verifiedAt)}`)).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByText('••••••••b7c1')).toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([
      { request: 'PUT /api/v1/settings/ai/api-key', body: { api_key: 'sk-ant-secret-b7c1' } },
      { request: 'POST /api/v1/settings/ai/verify', body: undefined },
    ])
    expect(document.body).not.toHaveTextContent('sk-ant-secret-b7c1')
  })

  it('shows the verify error and verifies again on request', async () => {
    let verifyCalls = 0
    stubAPI({
      'PUT /api/v1/settings/ai/api-key': () =>
        jsonResponse(200, { ...AI_SETTINGS, api_key_last4: 'b7c1', key_verified_at: null }),
      'POST /api/v1/settings/ai/verify': () => {
        verifyCalls++
        return verifyCalls === 1
          ? errorResponse(502, 'auth_failed')
          : jsonResponse(200, { ...AI_SETTINGS, api_key_last4: 'b7c1' })
      },
    })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }))
    const sheet = screen.getByRole('dialog', { name: 'Replace API key' })
    fireEvent.change(within(sheet).getByLabelText('API key'), { target: { value: 'sk-wrong-b7c1' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save and verify' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Verification failed: the provider rejected this key.')
    expect(screen.queryByText(/^Verified ·/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('Verified · Oct 1')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows a refused key in the sheet', async () => {
    stubAPI({
      'PUT /api/v1/settings/ai/api-key': () =>
        jsonResponse(400, {
          error: { code: 'invalid_settings', message: 'api_key: must be at least 8 characters', retryable: false },
        }),
    })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }))
    const sheet = screen.getByRole('dialog', { name: 'Replace API key' })
    fireEvent.change(within(sheet).getByLabelText('API key'), { target: { value: 'short' } })
    fireEvent.click(within(sheet).getByRole('button', { name: 'Save and verify' }))
    expect(await within(sheet).findByRole('alert')).toHaveTextContent(
      'The key was not stored: api_key: must be at least 8 characters',
    )
  })

  it('removes the stored key from the sheet', async () => {
    const fetchMock = stubAPI({
      'PUT /api/v1/settings/ai/api-key': () =>
        jsonResponse(200, { ...AI_SETTINGS, api_key_last4: null, key_verified_at: null }),
    })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove key' }))
    expect(await screen.findByText('No key stored')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add key' })).toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([{ request: 'PUT /api/v1/settings/ai/api-key', body: { api_key: '' } }])
  })

  it('resets the system prompt after confirming', async () => {
    const fetchMock = stubAPI({
      'POST /api/v1/settings/ai/reset-prompt': () => jsonResponse(200, { ...AI_SETTINGS, system_prompt: DEFAULT_PROMPT }),
    })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }))
    const confirm = screen.getByRole('dialog', { name: 'Reset the system prompt?' })
    fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
    expect(sent(fetchMock)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Reset' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reset prompt' }))
    await waitFor(() => expect(prompt()).toHaveValue(DEFAULT_PROMPT))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(sent(fetchMock)).toEqual([{ request: 'POST /api/v1/settings/ai/reset-prompt', body: undefined }])
    expect(save()).toBeDisabled()
  })

  it('asks before leaving with unsaved changes', async () => {
    stubAPI()
    await renderPage()
    fireEvent.change(prompt(), { target: { value: 'Unsaved.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    const confirm = await screen.findByRole('dialog', { name: 'Leave without saving?' })
    fireEvent.click(within(confirm).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(prompt()).toHaveValue('Unsaved.')

    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Leave' }))
    expect(await screen.findByTestId('path')).toHaveTextContent('/settings')
  })

  it('leaves without asking when nothing changed', async () => {
    stubAPI()
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Back to Settings' }))
    expect(await screen.findByTestId('path')).toHaveTextContent('/settings')
  })

  it('guards a page unload only while there are unsaved changes', async () => {
    stubAPI()
    await renderPage()
    const unload = () => {
      const event = new Event('beforeunload', { cancelable: true })
      act(() => void window.dispatchEvent(event))
      return event.defaultPrevented
    }
    expect(unload()).toBe(false)
    fireEvent.change(slider(), { target: { value: '0.2' } })
    expect(unload()).toBe(true)
  })

  it('keeps 44 px tap targets', async () => {
    stubAPI()
    await renderPage()
    for (const control of [
      screen.getByRole('button', { name: 'Back to Settings' }),
      screen.getByRole('button', { name: 'Replace' }),
      provider(),
      model(),
      slider(),
      screen.getByRole('button', { name: 'Reset' }),
      save(),
    ]) {
      expectMinHitArea(control)
    }
  })
})

describe('maskedKey', () => {
  it('shows only the last four characters', () => {
    expect(maskedKey('3f9a')).toBe('••••••••3f9a')
  })
})
