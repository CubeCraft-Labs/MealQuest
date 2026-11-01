import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from './api/index.ts'
import { DinnerDetailPage } from './dinners/DinnerDetailPage.tsx'
import { DiscoverPage } from './discover/DiscoverPage.tsx'
import { HomePage } from './home/HomePage.tsx'
import { CookFromPantryPage } from './ideas/CookFromPantryPage.tsx'
import { QuickDinnersPage } from './ideas/QuickDinnersPage.tsx'
import { OnboardingPage } from './onboarding/OnboardingPage.tsx'
import { createNetworkStatus } from './pwa/networkStatus.ts'
import { PlanPage } from './plan/PlanPage.tsx'
import { defaultWeek } from './plan/week.ts'
import { AISettingsPage } from './settings/AISettingsPage.tsx'
import { CalendarSettingsPage } from './settings/CalendarSettingsPage.tsx'
import { HouseholdSettingsPage } from './settings/HouseholdSettingsPage.tsx'
import { NotificationsPage } from './settings/NotificationsPage.tsx'
import { RegularsSettingsPage } from './settings/RegularsSettingsPage.tsx'
import { SettingsPage } from './settings/SettingsPage.tsx'
import { SupplyArchivePage } from './shopping/SupplyArchivePage.tsx'
import { SupplyRunPage } from './shopping/SupplyRunPage.tsx'
import {
  AI_MODELS,
  AI_SETTINGS,
  CHAT_WEBHOOK_NONE,
  GOOGLE_NOT_CONNECTED,
  HOME,
  HOUSEHOLD_SETTINGS,
  NOTIFICATION_SETTINGS,
  RECIPE_SEARCH_SETTINGS,
  SETTINGS_API,
} from './test/ApiProviders.tsx'
import { InDataRouter } from './test/dataRouter.tsx'
import { dinnerBody } from './test/dinners.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from './test/fetchStub.ts'
import { mockViewport } from './test/matchMedia.ts'
import skeletonStyles from './ui/Skeleton.module.css'

// The empty, loading and error state of every route, as docs/STATES.md
// lists them: each route renders its loading, empty and error variants
// against a stubbed API.

type Responder = () => Response | Promise<Response>

// pending is a response that never comes, so the page stays loading.
const pending: Responder = () => new Promise<Response>(() => {})

// failThen answers the first request with a 500, and every later one with
// then: Retry reads again and gets it.
function failThen(then: Responder): Responder {
  let failed = false
  return () => {
    if (failed) return then()
    failed = true
    return errorResponse(500, 'internal')
  }
}

const json = (body: unknown): Responder => () => jsonResponse(200, body)

// startJobs answers each POST that starts a job with the next job id, j1,
// j2 and so on.
function startJobs(): Responder {
  let n = 0
  return () => jsonResponse(202, { jobId: `j${++n}` })
}

// serve answers "METHOD path" requests from responders, with /me and the
// household settings; any other request fails the test.
function serve(responders: Record<string, Responder>) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': json(ME),
    'GET /api/v1/settings/household': json(HOUSEHOLD_SETTINGS),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond())
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// renderRoute renders element on its route pattern at path, under a data
// router as App renders it, once /me has loaded as AuthBootstrap loads it.
async function renderRoute(path: string, pattern: string, element: ReactNode) {
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <InDataRouter path={path}>
          <Routes>
            <Route path={pattern} element={element} />
          </Routes>
        </InDataRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

function skeletons() {
  return document.querySelectorAll(`.${skeletonStyles.skeleton}`)
}

// expectLoading waits for the pill skeletons and checks nothing failed.
async function expectLoading() {
  await waitFor(() => expect(skeletons().length).toBeGreaterThan(0))
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
}

// expectOnlyLoadingBefore checks, once a page's independent sections have
// settled, that the one skeleton left is the held read's: it sits before
// next, the first settled section after it, and nothing failed.
function expectOnlyLoadingBefore(next: Element) {
  expect(skeletons()).toHaveLength(1)
  const left = skeletons().item(0)
  if (left === null) throw new Error('no skeleton left')
  expect(next.contains(left)).toBe(false)
  expect(left.compareDocumentPosition(next) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
}

// expectRetry finds the error banner saying said, presses its Retry and
// waits for recovered.
async function expectRetry(said: string, recovered: () => Promise<unknown>) {
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(said)
  fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
  await recovered()
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
}

const WEEK = () => defaultWeek(HOUSEHOLD_SETTINGS, new Date())

beforeEach(() => {
  mockViewport(390)
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

describe('/ Home', () => {
  const NOTHING_TONIGHT = { ...HOME, tonight: null, sideQuestCount: 0, recentUpdates: { unread: false } }
  const page = async (home: Responder) => {
    serve({ 'GET /api/v1/home': home, 'GET /api/v1/rotation-reviews': json({ reviews: [] }) })
    await renderRoute('/', '/', <HomePage />)
  }

  it('loading: skeletons', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: no quest planned tonight, with Plan this week', async () => {
    await page(json(NOTHING_TONIGHT))
    expect(await screen.findByRole('heading', { name: 'No quest planned tonight' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Plan this week' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json(NOTHING_TONIGHT)))
    await expectRetry("Couldn't load tonight's quest. internal message", () =>
      screen.findByRole('heading', { name: 'No quest planned tonight' }),
    )
  })
})

describe('/onboarding Welcome step', () => {
  it('reads nothing, so it has no empty, loading or error state', async () => {
    const fetchMock = serve({})
    await renderRoute('/onboarding', '/onboarding/*', <OnboardingPage />)
    expect(screen.getByRole('heading', { name: 'Welcome to MealQuest' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: "Let's start" })).toBeEnabled()
    expect(skeletons()).toHaveLength(0)
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual(['/api/v1/me'])
  })
})

describe('/onboarding/regulars Regulars step', () => {
  const page = async (regulars: Responder) => {
    serve({ 'GET /api/v1/dinners?status=regular': regulars })
    await renderRoute('/onboarding/regulars', '/onboarding/*', <OnboardingPage />)
  }

  it('loading: skeleton', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: no Regulars yet, common picks offered and Next disabled', async () => {
    await page(json({ dinners: [] }))
    expect(await screen.findByRole('heading', { name: 'Common picks' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Your regulars · 0' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next: notifications' })).toBeDisabled()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json({ dinners: [] })))
    await expectRetry('Your Regulars could not load: internal message', () =>
      screen.findByRole('heading', { name: 'Common picks' }),
    )
  })
})

describe('/onboarding/notifications Notifications step', () => {
  const page = async (finish: Responder) => {
    serve({ 'PATCH /api/v1/settings/household': finish })
    await renderRoute('/onboarding/notifications', '/onboarding/*', <OnboardingPage />)
    fireEvent.click(screen.getByRole('button', { name: 'Maybe later' }))
  }

  it("loading: the step's buttons wait while it finishes", async () => {
    await page(pending)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Maybe later' })).toBeDisabled())
    expect(screen.getByRole('button', { name: 'Turn on notifications' })).toBeDisabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('error: says finishing failed and lets it be tried again', async () => {
    await page(failThen(pending))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not finish setting up: internal message')
    expect(screen.getByRole('button', { name: 'Maybe later' })).toBeEnabled()
  })
})

describe('/plan Draft board', () => {
  const page = async (plan: Responder) => {
    serve({ [`GET /api/v1/plans/${WEEK()}`]: plan, [`GET /api/v1/shopping/${WEEK()}`]: () => errorResponse(404, 'not_found') })
    await renderRoute('/plan', '/plan', <PlanPage />)
  }
  const noPlan = () => errorResponse(404, 'not_found')

  it('loading: skeleton tiles', async () => {
    await page(pending)
    expect(await screen.findByRole('region', { name: 'Plan loading' })).toBeInTheDocument()
    await expectLoading()
  })

  it('empty: nothing planned yet, with Draft this week', async () => {
    await page(noPlan)
    expect(await screen.findByRole('heading', { name: 'Nothing planned yet' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Draft this week' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(noPlan))
    await expectRetry("Couldn't load this week's plan. internal message", () =>
      screen.findByRole('heading', { name: 'Nothing planned yet' }),
    )
  })
})

describe('/plan/shopping Supply run', () => {
  const page = async (run: Responder) => {
    serve({ [`GET /api/v1/shopping/${WEEK()}`]: run })
    await renderRoute('/plan/shopping', '/plan/shopping', <SupplyRunPage />)
  }
  const noList = () => errorResponse(404, 'not_found')

  it('loading: skeleton cards', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: no shopping list yet, with Make the shopping list', async () => {
    await page(noList)
    expect(await screen.findByRole('heading', { name: 'No shopping list yet' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Make the shopping list' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(noList))
    await expectRetry("Couldn't load the shopping list. internal message", () =>
      screen.findByRole('heading', { name: 'No shopping list yet' }),
    )
  })
})

describe('/plan/shopping/archive Supply archive', () => {
  const page = async (archive: Responder) => {
    serve({ 'GET /api/v1/shopping/archive?by=week': archive })
    await renderRoute('/plan/shopping/archive', '/plan/shopping/archive', <SupplyArchivePage />)
  }
  const nothing = json({ by: 'week', periods: [] })

  it('loading: skeleton posts', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: nothing archived yet', async () => {
    await page(nothing)
    expect(await screen.findByRole('heading', { name: 'Nothing archived yet' })).toBeInTheDocument()
    expect(screen.getByText("Each week's Supply run becomes a post here once it's made.")).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(nothing))
    await expectRetry("Couldn't load the Supply archive. internal message", () =>
      screen.findByRole('heading', { name: 'Nothing archived yet' }),
    )
  })
})

describe('/discover Side quests', () => {
  const page = async (deck: Responder) => {
    serve({ 'GET /api/v1/suggestions/deck': deck, 'GET /api/v1/dinners?status=quest_log': json({ dinners: [] }) })
    await renderRoute('/discover', '/discover', <DiscoverPage />)
  }
  const empty = json({ suggestions: [], active_batch: null, failed_batch: null })

  it('loading: skeleton cards', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: All caught up / New side quests arrive Sunday., with Get more now', async () => {
    await page(empty)
    expect(await screen.findByRole('heading', { name: 'All caught up' })).toBeInTheDocument()
    expect(screen.getByText('New side quests arrive Sunday.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Get more now' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(empty))
    await expectRetry("Couldn't load your side quests. internal message", () =>
      screen.findByRole('heading', { name: 'All caught up' }),
    )
  })
})

describe('/quick Quick dinners', () => {
  const QUICK = 'POST /api/v1/ideas/quick'
  const job = (status: string, error: string | null) => ({
    id: 'j1',
    kind: 'quick_ideas',
    status,
    result: { stage: 'grounding', kind: 'under30', exclude: [], ideas: [], dropped: 0 },
    error,
  })
  const page = async (quick: Responder, poll: Responder = pending) => {
    serve({ [QUICK]: quick, 'GET /api/v1/jobs/j1': poll, 'GET /api/v1/jobs/j2': pending })
    await renderRoute('/quick', '/quick', <QuickDinnersPage />)
  }

  it('loading: skeleton rows', async () => {
    await page(pending)
    await waitFor(() => expect(screen.getAllByTestId('skeleton-row')).toHaveLength(6))
    await expectLoading()
  })

  it('empty: no ideas found, with Retry', async () => {
    await page(startJobs(), json(job('failed', 'no_ideas_found')))
    await expectRetry("Couldn't find Quick dinners. No ideas came back with a recipe to match.", () =>
      waitFor(() => expect(skeletons().length).toBeGreaterThan(0)),
    )
  })

  it('error: banner with Retry', async () => {
    await page(failThen(pending))
    await expectRetry("Couldn't start looking for Quick dinners. internal message", () =>
      waitFor(() => expect(screen.getAllByTestId('skeleton-row')).toHaveLength(6)),
    )
  })
})

describe('/cook-from-pantry Cook from what I have', () => {
  const FIND = 'POST /api/v1/ideas/from-ingredients'
  const job = {
    id: 'j1',
    kind: 'ideas_from_ingredients',
    status: 'failed',
    result: { stage: 'grounding', ingredients: ['Rice'], quick_only: false, ideas: [], dropped: 0 },
    error: 'no_ideas_found',
  }
  const page = async (find: Responder, poll: Responder = pending) => {
    serve({ [FIND]: find, 'GET /api/v1/jobs/j1': poll, 'GET /api/v1/jobs/j2': pending })
    await renderRoute('/cook-from-pantry', '/cook-from-pantry', <CookFromPantryPage />)
  }
  const search = () => {
    fireEvent.change(screen.getByRole('textbox', { name: 'Add ingredient' }), { target: { value: 'Rice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Find ideas' }))
  }

  it('loading: skeleton cards', async () => {
    await page(pending)
    search()
    expect(await screen.findByRole('region', { name: 'Ideas loading' })).toBeInTheDocument()
    await expectLoading()
  })

  it('empty: no ingredients yet, with Find ideas disabled; no ideas found, with Retry', async () => {
    await page(startJobs(), json(job))
    expect(screen.getByRole('button', { name: 'Find ideas' })).toBeDisabled()
    search()
    await expectRetry("Couldn't find ideas. No ideas came back with a recipe to match.", () =>
      screen.findByRole('region', { name: 'Ideas' }),
    )
  })

  it('error: banner with Retry', async () => {
    await page(failThen(pending))
    search()
    await expectRetry("Couldn't start looking for ideas. internal message", () =>
      screen.findByRole('region', { name: 'Ideas loading' }),
    )
  })
})

describe('/dinner/:id Dinner', () => {
  const page = async (dinner: Responder) => {
    serve({ 'GET /api/v1/dinners/d1': dinner })
    await renderRoute('/dinner/d1', '/dinner/:id', <DinnerDetailPage />)
  }
  const notFound = () => errorResponse(404, 'not_found')

  it('loading: skeletons', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: dinner not found', async () => {
    await page(notFound)
    expect(await screen.findByRole('heading', { name: 'Dinner not found' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(notFound))
    await expectRetry("Couldn't load this dinner. internal message", () =>
      screen.findByRole('heading', { name: 'Dinner not found' }),
    )
  })
})

describe('/settings Settings home', () => {
  const page = async (calendar: Responder) => {
    const api = Object.fromEntries(Object.entries(SETTINGS_API).map(([path, respond]) => [`GET ${path}`, respond]))
    serve({ ...api, 'GET /api/v1/dinners?status=regular': json({ dinners: [] }), 'GET /api/v1/integrations/google': calendar })
    await renderRoute('/settings', '/settings', <SettingsPage />)
  }
  const notConnected = json(GOOGLE_NOT_CONNECTED)

  it('loading: status skeletons', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: no Regulars, calendar not connected, Google Chat not connected', async () => {
    await page(notConnected)
    expect(await screen.findByText('None on rotation yet')).toBeInTheDocument()
    expect(await screen.findByText('Not connected')).toBeInTheDocument()
    expect(await screen.findByText(/Google Chat not connected/)).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(notConnected))
    await expectRetry('Settings could not load: internal message', () => screen.findByText('Not connected'))
  })
})

describe('/settings/notifications Notifications', () => {
  const page = async (prefs: Responder) => {
    serve({ 'GET /api/v1/settings/notifications': prefs, 'GET /api/v1/settings/chat-webhook': json(CHAT_WEBHOOK_NONE) })
    await renderRoute('/settings/notifications', '/settings/notifications', <NotificationsPage />)
  }

  it('loading: skeleton for the channel matrix', async () => {
    await page(pending)
    // The webhook card and this device's push status read on their own;
    // once they settle, the one skeleton left is the channel matrix's.
    await screen.findByRole('button', { name: 'Save webhook' })
    const device = screen.getByRole('group', { name: 'Notifications on this device' })
    await waitFor(() => expect(device.querySelectorAll(`.${skeletonStyles.skeleton}`)).toHaveLength(0))
    expectOnlyLoadingBefore(device)
    expect(screen.queryByRole('group', { name: 'Notification channels' })).not.toBeInTheDocument()
  })

  it('empty: no Google Chat webhook stored', async () => {
    await page(json(NOTIFICATION_SETTINGS))
    expect(await screen.findByRole('button', { name: 'Save webhook' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json(NOTIFICATION_SETTINGS)))
    await expectRetry('Notification settings could not load: internal message', () =>
      screen.findByRole('group', { name: 'Notification channels' }),
    )
  })
})

describe('/settings/ai AI assistant', () => {
  const NO_KEY = { ...AI_SETTINGS, api_key_last4: null, key_verified_at: null }
  const page = async (settings: Responder) => {
    serve({
      'GET /api/v1/settings/ai': settings,
      'GET /api/v1/settings/ai/models': json(AI_MODELS),
      'GET /api/v1/settings/recipe-search': json(RECIPE_SEARCH_SETTINGS),
    })
    await renderRoute('/settings/ai', '/settings/ai', <AISettingsPage />)
  }

  it('loading: skeleton for the AI assistant form', async () => {
    await page(pending)
    // The Recipe search card reads on its own; once it settles, the one
    // skeleton left is the AI assistant form's.
    await screen.findByRole('button', { name: 'Save recipe search' })
    expectOnlyLoadingBefore(screen.getByRole('region', { name: 'Recipe search' }))
    expect(screen.queryByText('No key stored')).not.toBeInTheDocument()
  })

  it('empty: no key stored, with Add key', async () => {
    await page(json(NO_KEY))
    expect(await screen.findByText('No key stored')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add key' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json(NO_KEY)))
    await expectRetry('AI settings could not load: internal message', () => screen.findByText('No key stored'))
  })
})

describe('/settings/household Household', () => {
  const page = async (settings: Responder) => {
    serve({ 'GET /api/v1/settings/household': settings })
    await renderRoute('/settings/household', '/settings/household', <HouseholdSettingsPage />)
  }

  it('loading: skeleton', async () => {
    await page(pending)
    await expectLoading()
  })

  // The household's settings always exist (seeded on first start), so its
  // "empty" state is the seeded defaults.
  it('empty: the seeded settings', async () => {
    await page(json(HOUSEHOLD_SETTINGS))
    expect(await screen.findByLabelText('Dinner time')).toHaveValue('17:30')
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json(HOUSEHOLD_SETTINGS)))
    await expectRetry('Household settings could not load: internal message', () => screen.findByLabelText('Dinner time'))
  })
})

describe('/settings/regulars Regular dinners', () => {
  const page = async (regulars: Responder) => {
    serve({ 'GET /api/v1/dinners?status=regular': regulars })
    await renderRoute('/settings/regulars', '/settings/regulars', <RegularsSettingsPage />)
  }
  const none = json({ dinners: [] })

  it('loading: skeleton', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: no Regulars yet', async () => {
    await page(none)
    expect(await screen.findByText('No Regulars yet. Add the dinners your household makes on repeat.')).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(json({ dinners: [dinnerBody('d1', 'Beef tacos')] })))
    await expectRetry('Your Regulars could not load: internal message', () => screen.findByText('Beef tacos'))
  })
})

describe('/settings/calendar Calendar', () => {
  const page = async (connection: Responder) => {
    serve({ 'GET /api/v1/integrations/google': connection })
    await renderRoute('/settings/calendar', '/settings/calendar', <CalendarSettingsPage />)
  }
  const notConnected = json(GOOGLE_NOT_CONNECTED)

  it('loading: skeleton', async () => {
    await page(pending)
    await expectLoading()
  })

  it('empty: not connected, with Connect Google Calendar', async () => {
    await page(notConnected)
    expect(await screen.findByRole('heading', { name: 'Not connected' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Connect Google Calendar' })).toBeInTheDocument()
  })

  it('error: banner with Retry', async () => {
    await page(failThen(notConnected))
    await expectRetry('Google Calendar status could not load: internal message', () =>
      screen.findByRole('heading', { name: 'Not connected' }),
    )
  })
})
