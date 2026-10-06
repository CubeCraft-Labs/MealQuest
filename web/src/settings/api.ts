import { useQuery } from '@tanstack/react-query'
import { ApiError, useApi, type ApiClient } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'

export const HOUSEHOLD_SETTINGS_PATH = '/api/v1/settings/household'
export const AI_SETTINGS_PATH = '/api/v1/settings/ai'
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
// settings pages read.
export type HouseholdSettings = {
  timezone: string
  // Zero-padded 24-hour HH:MM.
  dinner_time: string
}

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

// AISettings is the part of GET /api/v1/settings/ai the settings pages read.
export type AISettings = {
  provider: AIProvider
  model: string
}

function isAIProvider(value: string): value is AIProvider {
  return Object.hasOwn(aiProviderLabels, value)
}

const clockTime = /^([01][0-9]|2[0-3]):([0-5][0-9])$/

// useHouseholdSettings refuses a dinner time that is not HH:MM, so
// formatClockTime never sees one.
export function useHouseholdSettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['settings', 'household'],
    queryFn: async () => {
      const settings = await apiFetch<HouseholdSettings>(HOUSEHOLD_SETTINGS_PATH)
      if (!clockTime.test(settings.dinner_time)) {
        throw new Error(`GET ${HOUSEHOLD_SETTINGS_PATH}: dinner_time "${settings.dinner_time}" is not HH:MM`)
      }
      return { timezone: settings.timezone, dinner_time: settings.dinner_time } satisfies HouseholdSettings
    },
  })
}

// useAISettings refuses a provider this client has no name for, so the
// settings never show a value the household cannot read.
export function useAISettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['settings', 'ai'],
    queryFn: async () => {
      const settings = await apiFetch<{ provider: string; model: string }>(AI_SETTINGS_PATH)
      if (!isAIProvider(settings.provider)) {
        throw new Error(`GET ${AI_SETTINGS_PATH}: unknown AI provider "${settings.provider}"`)
      }
      return { provider: settings.provider, model: settings.model } satisfies AISettings
    },
  })
}

// aiStatus is the AI assistant row's status line: the provider and the
// model's display name, or that no model is chosen yet (the seed).
export function aiStatus(settings: AISettings): string {
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

// formatClockTime turns a 24-hour HH:MM time into the handoff's "5:30 pm".
export function formatClockTime(time: string): string {
  const match = clockTime.exec(time)
  if (match === null) throw new Error(`not a 24-hour HH:MM time: "${time}"`)
  const hours = Number(match[1])
  const suffix = hours < 12 ? 'am' : 'pm'
  return `${hours % 12 === 0 ? 12 : hours % 12}:${match[2]} ${suffix}`
}
