import { QueryClientProvider } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { errorResponse, fakeLocation, jsonResponse } from './fetchStub.ts'

// AI_SETTINGS is a GET /api/v1/settings/ai body: Anthropic with a verified key.
export const AI_SETTINGS = {
  provider: 'anthropic',
  base_url: '',
  model: 'claude-sonnet-4-5',
  temperature: 0.7,
  system_prompt: 'You are MealQuest, a dinner planning assistant for one household.',
  api_key_last4: '3f9a',
  key_verified_at: '2026-10-01T15:00:00.000Z',
  updated_at: '2026-10-01T15:00:00.000Z',
}

// AI_MODELS is a GET /api/v1/settings/ai/models body: the model AI_SETTINGS
// chooses.
export const AI_MODELS = { models: [{ id: 'claude-sonnet-4-5', display_name: 'Claude Sonnet' }] }

// HOUSEHOLD_SETTINGS is a GET /api/v1/settings/household body: the seed
// times in New York, onboarding finished.
export const HOUSEHOLD_SETTINGS = {
  timezone: 'America/New_York',
  dinner_time: '17:30',
  reminder_time: '16:30',
  review_time: '09:00',
  plan_draft_dow: 5,
  plan_draft_time: '09:00',
  suggestions_dow: 0,
  suggestions_time: '08:00',
  chat_autosend_dow: 6,
  chat_autosend_time: '08:00',
  onboarding_completed_at: '2026-10-01T15:30:00.000Z',
  updates_seen_at: null,
  updated_by: null,
  created_at: '2026-10-01T15:00:00.000Z',
  updated_at: '2026-10-01T15:00:00.000Z',
}

// RECIPE_SEARCH_SETTINGS is a GET /api/v1/settings/recipe-search body: the
// SearXNG sidecar, no Brave key stored.
export const RECIPE_SEARCH_SETTINGS = {
  provider: 'searxng',
  base_url: 'http://searxng:8080',
  default_base_url: 'http://searxng:8080',
  api_key_set: false,
  api_key_last4: null,
  updated_at: '2026-10-01T15:00:00.000Z',
}

// GOOGLE_NOT_CONNECTED is a GET /api/v1/integrations/google body: nothing
// connected.
export const GOOGLE_NOT_CONNECTED = {
  status: 'not_connected',
  calendar_id: null,
  calendar_name: null,
  connected_at: null,
  last_sync_error: null,
}

// NOTIFICATION_SETTINGS is a GET /api/v1/settings/notifications body: the
// seed channels and reminder time.
export const NOTIFICATION_SETTINGS = {
  prefs: [
    { event: 'plan_ready', push: true, chat: true },
    { event: 'list_ready', push: true, chat: true },
    { event: 'day_of_reminder', push: true, chat: false },
    { event: 'new_suggestions', push: true, chat: false },
    { event: 'rotation_review', push: true, chat: false },
  ],
  reminder_time: '16:30',
}

// CHAT_WEBHOOK_NONE is a GET /api/v1/settings/chat-webhook body: no webhook
// stored.
export const CHAT_WEBHOOK_NONE = { space_label: null, url_masked: null, last_test_at: null, last_test_status: null }

// CHAT_WEBHOOK is a GET /api/v1/settings/chat-webhook body: a webhook
// stored and never tested.
export const CHAT_WEBHOOK = {
  space_label: 'Dinner Crew space',
  url_masked: 'https://chat.googleapis.com/v1/spaces/AAQA1b2C3d4/messages?token=…qQ%3D',
  last_test_at: null,
  last_test_status: null,
}

// SETTINGS_API answers the settings home's requests, for stubFetchByPath, as
// today's server does: the dinners endpoint does not exist yet, and neither
// Google Calendar nor Google Chat is connected. The home's sub-tabs stay mounted on mobile, so
// it also reads the AI models and recipe search settings.
export const SETTINGS_API = {
  '/api/v1/dinners?status=regular': () => errorResponse(404, 'not_found'),
  '/api/v1/integrations/google': () => jsonResponse(200, GOOGLE_NOT_CONNECTED),
  '/api/v1/settings/notifications': () => jsonResponse(200, NOTIFICATION_SETTINGS),
  '/api/v1/settings/chat-webhook': () => jsonResponse(200, CHAT_WEBHOOK_NONE),
  '/api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
  '/api/v1/settings/ai': () => jsonResponse(200, AI_SETTINGS),
  '/api/v1/settings/ai/models': () => jsonResponse(200, AI_MODELS),
  '/api/v1/settings/recipe-search': () => jsonResponse(200, RECIPE_SEARCH_SETTINGS),
}

// ApiProviders gives the pages under test what App gives them: an API client
// on https://mealquest.test and a fresh query client.
export function ApiProviders({ children }: { children: ReactNode }) {
  const [api] = useState(() =>
    createApiClient(fakeLocation(), createNetworkStatus(window), createVerifiedSession(localStorage)),
  )
  const [queryClient] = useState(createQueryClient)
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </ApiProvider>
  )
}
