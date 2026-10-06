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

// SETTINGS_API answers the settings home's requests, for stubFetchByPath, as
// today's server does: the dinners, Google Calendar and notifications status
// endpoints do not exist yet.
export const SETTINGS_API = {
  '/api/v1/dinners?status=regular': () => errorResponse(404, 'not_found'),
  '/api/v1/settings/calendar': () => errorResponse(404, 'not_found'),
  '/api/v1/settings/notifications': () => errorResponse(404, 'not_found'),
  '/api/v1/settings/household': () => jsonResponse(200, { timezone: 'America/New_York', dinner_time: '17:30' }),
  '/api/v1/settings/ai': () => jsonResponse(200, AI_SETTINGS),
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
