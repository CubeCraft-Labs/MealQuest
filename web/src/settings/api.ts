import { useQuery } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'

export const HOUSEHOLD_SETTINGS_PATH = '/api/v1/settings/household'
export const AI_SETTINGS_PATH = '/api/v1/settings/ai'

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

// formatClockTime turns a 24-hour HH:MM time into the handoff's "5:30 pm".
export function formatClockTime(time: string): string {
  const match = clockTime.exec(time)
  if (match === null) throw new Error(`not a 24-hour HH:MM time: "${time}"`)
  const hours = Number(match[1])
  const suffix = hours < 12 ? 'am' : 'pm'
  return `${hours % 12 === 0 ? 12 : hours % 12}:${match[2]} ${suffix}`
}
