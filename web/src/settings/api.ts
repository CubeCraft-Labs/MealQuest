import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi, type ApiClient } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'

export const HOUSEHOLD_SETTINGS_PATH = '/api/v1/settings/household'
export const AI_SETTINGS_PATH = '/api/v1/settings/ai'
export const AI_MODELS_PATH = '/api/v1/settings/ai/models'
export const AI_KEY_PATH = '/api/v1/settings/ai/api-key'
export const AI_VERIFY_PATH = '/api/v1/settings/ai/verify'
export const AI_RESET_PROMPT_PATH = '/api/v1/settings/ai/reset-prompt'
export const RECIPE_SEARCH_PATH = '/api/v1/settings/recipe-search'
export const RECIPE_SEARCH_TEST_PATH = '/api/v1/settings/recipe-search/test'
// TODO(Feature 12): GET /api/v1/dinners?status=regular is Feature 12's
// dinners read API. Until it lands the server answers 404 and the Regular
// dinners row says the count is unavailable.
export const REGULAR_DINNERS_PATH = '/api/v1/dinners?status=regular'
// TODO(Feature 16): the Google Calendar integration status. Until Feature 16
// serves it the server answers 404, which is the truth: not connected.
export const CALENDAR_STATUS_PATH = '/api/v1/settings/calendar'
// TODO(notifications feature): the Web Push and Google Chat status. Until it
// is served the server answers 404, which is the truth: push is off and no
// Google Chat space is connected.
export const NOTIFICATIONS_STATUS_PATH = '/api/v1/settings/notifications'

// HouseholdSettings is the part of GET /api/v1/settings/household the
// settings pages read. Times are zero-padded 24-hour HH:MM in timezone; days
// of the week count from Sunday = 0.
export type HouseholdSettings = {
  timezone: string
  dinner_time: string
  reminder_time: string
  // The rotation check: when the household is asked "Keep it in rotation?".
  review_time: string
  plan_draft_dow: number
  plan_draft_time: string
  suggestions_dow: number
  suggestions_time: string
  chat_autosend_dow: number
  chat_autosend_time: string
  // When the household finished onboarding; null until it has, and every
  // app route sends it to /onboarding. Not editable in settings.
  onboarding_completed_at: string | null
}

// HouseholdSettingsField is a household setting the settings pages edit.
export type HouseholdSettingsField = Exclude<keyof HouseholdSettings, 'onboarding_completed_at'>

// HouseholdSettingsPatch is the PATCH /api/v1/settings/household body: only
// the fields that change.
export type HouseholdSettingsPatch = Partial<Pick<HouseholdSettings, HouseholdSettingsField>>

const householdTimeFields = [
  'dinner_time',
  'reminder_time',
  'review_time',
  'plan_draft_time',
  'suggestions_time',
  'chat_autosend_time',
] as const satisfies readonly HouseholdSettingsField[]

const householdDayFields = [
  'plan_draft_dow',
  'suggestions_dow',
  'chat_autosend_dow',
] as const satisfies readonly HouseholdSettingsField[]

const householdFields: readonly HouseholdSettingsField[] = ['timezone', ...householdTimeFields, ...householdDayFields]

// aiProviderLabels names every AI provider preset the server accepts
// (internal/ai/llm/presets) as the household reads it.
export const aiProviderLabels = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  openrouter: 'OpenRouter',
  xai: 'xAI',
  ollama: 'Ollama',
  openai_compatible: 'OpenAI-compatible',
} as const

export type AIProvider = keyof typeof aiProviderLabels

// providersWithBaseURL are the presets with no fixed API root: the
// household supplies its base URL.
export const providersWithBaseURL: ReadonlySet<AIProvider> = new Set(['ollama', 'openai_compatible'])

// aiKeyPolicy is whether each preset needs an API key, may take one, or
// takes none (internal/ai/llm/presets).
export const aiKeyPolicy: Record<AIProvider, 'required' | 'optional' | 'none'> = {
  anthropic: 'required',
  openai: 'required',
  openrouter: 'required',
  xai: 'required',
  ollama: 'none',
  openai_compatible: 'optional',
}

// AISettings is GET /api/v1/settings/ai. The API key itself is never sent:
// only its last four characters, null when none is stored.
export type AISettings = {
  provider: AIProvider
  base_url: string
  model: string
  temperature: number
  // Null when none is stored.
  system_prompt: string | null
  api_key_last4: string | null
  // When the stored key was last verified, null when it has not been.
  key_verified_at: string | null
  updated_at: string
}

// AISettingsPatch is the PATCH /api/v1/settings/ai body: only the fields
// that change.
export type AISettingsPatch = Partial<Pick<AISettings, 'provider' | 'base_url' | 'model' | 'temperature'>> & {
  system_prompt?: string
}

// AIModel is one model GET /api/v1/settings/ai/models lists.
export type AIModel = { id: string; display_name: string }

function isAIProvider(value: string): value is AIProvider {
  return Object.hasOwn(aiProviderLabels, value)
}

function isStringOrNull(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

// parseAISettings refuses a body that is not the AI settings, or names a
// provider this client has no name for, so the settings never show a value
// the household cannot read.
function parseAISettings(where: string, body: unknown): AISettings {
  if (
    !isRecord(body) ||
    typeof body.provider !== 'string' ||
    typeof body.base_url !== 'string' ||
    typeof body.model !== 'string' ||
    typeof body.temperature !== 'number' ||
    !isStringOrNull(body.system_prompt) ||
    !isStringOrNull(body.api_key_last4) ||
    !isStringOrNull(body.key_verified_at) ||
    typeof body.updated_at !== 'string'
  ) {
    throw new Error(`${where}: response is not the AI settings`)
  }
  if (!isAIProvider(body.provider)) throw new Error(`${where}: unknown AI provider "${body.provider}"`)
  return {
    provider: body.provider,
    base_url: body.base_url,
    model: body.model,
    temperature: body.temperature,
    system_prompt: body.system_prompt,
    api_key_last4: body.api_key_last4,
    key_verified_at: body.key_verified_at,
    updated_at: body.updated_at,
  }
}

const AI_SETTINGS_KEY = ['settings', 'ai'] as const
const AI_MODELS_KEY = ['settings', 'ai', 'models'] as const

export function useAISettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: AI_SETTINGS_KEY,
    queryFn: async () => parseAISettings(`GET ${AI_SETTINGS_PATH}`, await apiFetch<unknown>(AI_SETTINGS_PATH)),
  })
}

// useAIModels is the saved provider's live model list. Without a usable
// provider (no key yet, say) it fails with the server's 409 not_configured.
export function useAIModels(enabled: boolean) {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: AI_MODELS_KEY,
    enabled,
    queryFn: async () => {
      const body = await apiFetch<unknown>(AI_MODELS_PATH)
      const models = isRecord(body) ? body.models : undefined
      if (
        !Array.isArray(models) ||
        !models.every((m) => isRecord(m) && typeof m.id === 'string' && typeof m.display_name === 'string')
      ) {
        throw new Error(`GET ${AI_MODELS_PATH}: response is not a model list`)
      }
      return models as AIModel[]
    },
  })
}

// useAISettingsMutation sends one AI settings change and stores the
// settings the server answers with. A change that can alter which models
// the provider offers refetches the model list.
function useAISettingsMutation<V>(path: string, method: 'POST' | 'PUT' | 'PATCH', body: (variables: V) => unknown) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (variables: V) =>
      parseAISettings(`${method} ${path}`, await apiFetch<unknown>(path, { method, body: body(variables) })),
    onSuccess: (settings) => {
      const before = queryClient.getQueryData<AISettings>(AI_SETTINGS_KEY)
      queryClient.setQueryData(AI_SETTINGS_KEY, settings)
      if (
        before === undefined ||
        before.provider !== settings.provider ||
        before.base_url !== settings.base_url ||
        before.api_key_last4 !== settings.api_key_last4 ||
        before.key_verified_at !== settings.key_verified_at
      ) {
        void queryClient.invalidateQueries({ queryKey: AI_MODELS_KEY })
      }
    },
  })
}

export function useSaveAISettings() {
  return useAISettingsMutation(AI_SETTINGS_PATH, 'PATCH', (patch: AISettingsPatch) => patch)
}

// useSetAIKey stores a new API key; an empty key removes the stored one.
export function useSetAIKey() {
  return useAISettingsMutation(AI_KEY_PATH, 'PUT', (apiKey: string) => ({ api_key: apiKey }))
}

export function useVerifyAIKey() {
  return useAISettingsMutation(AI_VERIFY_PATH, 'POST', () => undefined)
}

export function useResetAIPrompt() {
  return useAISettingsMutation(AI_RESET_PROMPT_PATH, 'POST', () => undefined)
}

// recipeSearchProviderLabels names every recipe search provider the server
// accepts (internal/recipes/searchsettings.go).
export const recipeSearchProviderLabels = {
  searxng: 'SearXNG',
  brave: 'Brave',
} as const

export type RecipeSearchProvider = keyof typeof recipeSearchProviderLabels

// RecipeSearchSettings is GET /api/v1/settings/recipe-search. The Brave API
// key itself is never sent: only whether one is stored and its last four
// characters.
export type RecipeSearchSettings = {
  // Null until recipe search is first set up.
  provider: RecipeSearchProvider | null
  // The saved SearXNG instance's URL, null when none is set.
  base_url: string | null
  // The deployment's SearXNG URL (MEALQUEST_SEARXNG_URL) to suggest while
  // base_url is null; null when the deployment names none. Only saving
  // base_url stores a URL.
  default_base_url: string | null
  api_key_set: boolean
  api_key_last4: string | null
  updated_at: string | null
}

// RecipeSearchSettingsPatch is the PATCH /api/v1/settings/recipe-search
// body: only the fields that change. api_key is the Brave key in plain text.
export type RecipeSearchSettingsPatch = {
  provider?: RecipeSearchProvider
  base_url?: string
  api_key?: string
}

// RecipeSearchTest is POST /api/v1/settings/recipe-search/test: how many
// recipes the saved provider found for query.
export type RecipeSearchTest = { query: string; count: number }

function isRecipeSearchProvider(value: string): value is RecipeSearchProvider {
  return Object.hasOwn(recipeSearchProviderLabels, value)
}

function parseRecipeSearchSettings(where: string, body: unknown): RecipeSearchSettings {
  if (
    !isRecord(body) ||
    !isStringOrNull(body.provider) ||
    !isStringOrNull(body.base_url) ||
    !isStringOrNull(body.default_base_url) ||
    typeof body.api_key_set !== 'boolean' ||
    !isStringOrNull(body.api_key_last4) ||
    !isStringOrNull(body.updated_at)
  ) {
    throw new Error(`${where}: response is not the recipe search settings`)
  }
  if (body.provider !== null && !isRecipeSearchProvider(body.provider)) {
    throw new Error(`${where}: unknown recipe search provider "${body.provider}"`)
  }
  return {
    provider: body.provider,
    base_url: body.base_url,
    default_base_url: body.default_base_url,
    api_key_set: body.api_key_set,
    api_key_last4: body.api_key_last4,
    updated_at: body.updated_at,
  }
}

const RECIPE_SEARCH_KEY = ['settings', 'recipe-search'] as const

export function useRecipeSearchSettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: RECIPE_SEARCH_KEY,
    queryFn: async () =>
      parseRecipeSearchSettings(`GET ${RECIPE_SEARCH_PATH}`, await apiFetch<unknown>(RECIPE_SEARCH_PATH)),
  })
}

export function useSaveRecipeSearchSettings() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (patch: RecipeSearchSettingsPatch) =>
      parseRecipeSearchSettings(
        `PATCH ${RECIPE_SEARCH_PATH}`,
        await apiFetch<unknown>(RECIPE_SEARCH_PATH, { method: 'PATCH', body: patch }),
      ),
    onSuccess: (settings) => queryClient.setQueryData(RECIPE_SEARCH_KEY, settings),
  })
}

// useTestRecipeSearch searches with the saved settings.
export function useTestRecipeSearch() {
  const { apiFetch } = useApi()
  return useMutation({
    mutationFn: async () => {
      const body = await apiFetch<unknown>(RECIPE_SEARCH_TEST_PATH, { method: 'POST' })
      if (!isRecord(body) || typeof body.query !== 'string' || typeof body.count !== 'number') {
        throw new Error(`POST ${RECIPE_SEARCH_TEST_PATH}: response is not a search test result`)
      }
      return { query: body.query, count: body.count } satisfies RecipeSearchTest
    },
  })
}

const clockTime = /^([01][0-9]|2[0-3]):([0-5][0-9])$/

// parseHouseholdSettings refuses a body that is not the household settings,
// or has a time that is not HH:MM, so formatClockTime never sees one.
function parseHouseholdSettings(where: string, body: unknown): HouseholdSettings {
  if (!isRecord(body) || typeof body.timezone !== 'string') {
    throw new Error(`${where}: response is not the household settings`)
  }
  const times = {} as Record<(typeof householdTimeFields)[number], string>
  for (const field of householdTimeFields) {
    const value = body[field]
    if (typeof value !== 'string' || !clockTime.test(value)) {
      throw new Error(`${where}: ${field} ${JSON.stringify(value)} is not HH:MM`)
    }
    times[field] = value
  }
  const days = {} as Record<(typeof householdDayFields)[number], number>
  for (const field of householdDayFields) {
    const value = body[field]
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 6) {
      throw new Error(`${where}: ${field} ${JSON.stringify(value)} is not a day of the week (0-6)`)
    }
    days[field] = value
  }
  if (!isStringOrNull(body.onboarding_completed_at)) {
    throw new Error(`${where}: onboarding_completed_at ${JSON.stringify(body.onboarding_completed_at)} is not a time or null`)
  }
  return { timezone: body.timezone, ...times, ...days, onboarding_completed_at: body.onboarding_completed_at }
}

export const HOUSEHOLD_SETTINGS_KEY = ['settings', 'household'] as const

// useHouseholdSettings runs even while the browser reports offline, like
// /me: OnboardingGate reads it before every page, so offline it must fail
// into the saved pages instead of waiting for the network.
export function useHouseholdSettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: HOUSEHOLD_SETTINGS_KEY,
    queryFn: async () =>
      parseHouseholdSettings(`GET ${HOUSEHOLD_SETTINGS_PATH}`, await apiFetch<unknown>(HOUSEHOLD_SETTINGS_PATH)),
    networkMode: 'always',
  })
}

export function useSaveHouseholdSettings() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (patch: HouseholdSettingsPatch) =>
      parseHouseholdSettings(
        `PATCH ${HOUSEHOLD_SETTINGS_PATH}`,
        await apiFetch<unknown>(HOUSEHOLD_SETTINGS_PATH, { method: 'PATCH', body: patch }),
      ),
    onSuccess: (settings) => queryClient.setQueryData(HOUSEHOLD_SETTINGS_KEY, settings),
  })
}

// useCompleteOnboarding records that the household finished onboarding: the
// server sets onboarding_completed_at, which ends the first-run redirect.
export function useCompleteOnboarding() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async () =>
      parseHouseholdSettings(
        `PATCH ${HOUSEHOLD_SETTINGS_PATH}`,
        await apiFetch<unknown>(HOUSEHOLD_SETTINGS_PATH, { method: 'PATCH', body: { onboarding_completed: true } }),
      ),
    onSuccess: (settings) => queryClient.setQueryData(HOUSEHOLD_SETTINGS_KEY, settings),
  })
}

// HouseholdSettingsProblems is a refused household settings save: the reason
// for each field the server named, and any reasons that name no field.
export type HouseholdSettingsProblems = {
  fields: Partial<Record<HouseholdSettingsField, string>>
  other: string[]
}

const invalidHouseholdPrefix = 'invalid household settings: '

// A problem starts with a field name and ": " (internal/household
// ValidationError.Error joins them with "; ").
const problemStart = /; (?=[a-z_]+: )/

// householdSettingsProblems reads the server's invalid_settings error into
// the problems it lists. Any other error is one problem naming no field.
export function householdSettingsProblems(error: Error): HouseholdSettingsProblems {
  const problems: HouseholdSettingsProblems = { fields: {}, other: [] }
  if (
    !(error instanceof ApiError) ||
    error.code !== 'invalid_settings' ||
    !error.message.startsWith(invalidHouseholdPrefix)
  ) {
    problems.other.push(error.message)
    return problems
  }
  for (const problem of error.message.slice(invalidHouseholdPrefix.length).split(problemStart)) {
    const colon = problem.indexOf(': ')
    const field = problem.slice(0, colon)
    if (colon !== -1 && (householdFields as readonly string[]).includes(field)) {
      problems.fields[field as HouseholdSettingsField] = problem.slice(colon + 2)
    } else {
      problems.other.push(problem)
    }
  }
  return problems
}

// needsMoreRegulars reports whether error is the server refusing to finish
// onboarding because the household has fewer than three Regulars
// (internal/household MinRegulars) — the only refusal of
// {"onboarding_completed": true}.
export function needsMoreRegulars(error: Error): boolean {
  return (
    error instanceof ApiError &&
    error.code === 'invalid_settings' &&
    error.message.startsWith(`${invalidHouseholdPrefix}onboarding_completed: `)
  )
}

// aiStatus is the AI assistant row's status line: the provider and the
// model's display name, or that no model is chosen yet (the seed).
export function aiStatus(settings: Pick<AISettings, 'provider' | 'model'>): string {
  const provider = aiProviderLabels[settings.provider]
  if (settings.model === '') return `${provider} · No model chosen`
  return `${provider} · ${modelDisplayName(settings.model)}`
}

// Claude model ids, current ("claude-sonnet-4-5") and older
// ("claude-3-5-sonnet-20241022"), name their family.
const claudeModel = /^claude-(?:[0-9-]+-)?(opus|sonnet|haiku|fable)(?:-|$)/
const claudeFamilies: Record<string, string> = { opus: 'Opus', sonnet: 'Sonnet', haiku: 'Haiku', fable: 'Fable' }

// modelDisplayName names a model as the handoff does ("Claude Sonnet"); a
// model with no known family is shown by its id.
export function modelDisplayName(model: string): string {
  const family = claudeModel.exec(model)?.[1]
  return family === undefined ? model : `Claude ${claudeFamilies[family]}`
}

// fetchUnlessAbsent GETs path and parses it, or returns null when the server
// has no such endpoint yet (404 not_found). Every other failure is an error.
async function fetchUnlessAbsent<T>(
  apiFetch: ApiClient['apiFetch'],
  path: string,
  parse: (body: unknown) => T,
): Promise<T | null> {
  let body: unknown
  try {
    body = await apiFetch<unknown>(path)
  } catch (error) {
    if (error instanceof ApiError && error.status === 404 && error.code === 'not_found') return null
    throw error
  }
  return parse(body)
}

// useRegularDinnersCount is how many Regulars the household has, or null
// while the dinners read API does not exist.
export function useRegularDinnersCount() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['dinners', 'regular', 'count'],
    queryFn: () =>
      fetchUnlessAbsent(apiFetch, REGULAR_DINNERS_PATH, (body) => {
        if (!isRecord(body) || !Array.isArray(body.dinners)) {
          throw new Error(`GET ${REGULAR_DINNERS_PATH}: response has no dinners list`)
        }
        return body.dinners.length
      }),
  })
}

export function regularDinnersStatus(count: number | null): string {
  if (count === null) return 'Rotation count unavailable'
  if (count === 0) return 'None on rotation yet'
  return `${count} on rotation`
}

// CalendarStatus is the Google Calendar integration status; calendar_name
// is the connected calendar's name.
export type CalendarStatus = { connected: false } | { connected: true; calendar_name: string }

export function useCalendarStatus() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['settings', 'calendar'],
    queryFn: () =>
      fetchUnlessAbsent(apiFetch, CALENDAR_STATUS_PATH, (body): CalendarStatus => {
        if (isRecord(body) && body.connected === false) return { connected: false }
        if (isRecord(body) && body.connected === true && typeof body.calendar_name === 'string') {
          return { connected: true, calendar_name: body.calendar_name }
        }
        throw new Error(`GET ${CALENDAR_STATUS_PATH}: response is not a calendar status`)
      }),
  })
}

export function calendarStatus(status: CalendarStatus | null): string {
  if (status === null || !status.connected) return 'Not connected'
  return `Connected · ${status.calendar_name}`
}

// NotificationsStatus is whether Web Push is on and the connected Google
// Chat space's name, null when none is.
export type NotificationsStatus = { push: boolean; chat_space: string | null }

export function useNotificationsStatus() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['settings', 'notifications'],
    queryFn: () =>
      fetchUnlessAbsent(apiFetch, NOTIFICATIONS_STATUS_PATH, (body): NotificationsStatus => {
        if (
          !isRecord(body) ||
          typeof body.push !== 'boolean' ||
          (body.chat_space !== null && typeof body.chat_space !== 'string')
        ) {
          throw new Error(`GET ${NOTIFICATIONS_STATUS_PATH}: response is not a notifications status`)
        }
        return { push: body.push, chat_space: body.chat_space }
      }),
  })
}

// notificationsStatus is the handoff's "Push on · Dinner Crew space".
export function notificationsStatus(status: NotificationsStatus | null): string {
  const push = status?.push === true ? 'Push on' : 'Push off'
  return `${push} · ${status?.chat_space ?? 'Google Chat not connected'}`
}

// maskedKey shows a stored key as dots and its last four characters, all
// the client ever knows of it.
export function maskedKey(last4: string): string {
  return `••••••••${last4}`
}

// formatClockTime turns a 24-hour HH:MM time into the handoff's "5:30 pm".
export function formatClockTime(time: string): string {
  const match = clockTime.exec(time)
  if (match === null) throw new Error(`not a 24-hour HH:MM time: "${time}"`)
  const hours = Number(match[1])
  const suffix = hours < 12 ? 'am' : 'pm'
  return `${hours % 12 === 0 ? 12 : hours % 12}:${match[2]} ${suffix}`
}
