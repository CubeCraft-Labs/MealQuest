import { QueryClientProvider } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { fakeLocation, jsonResponse } from './fetchStub.ts'

// SETTINGS_API answers the settings home's requests, for stubFetchByPath.
export const SETTINGS_API = {
  '/api/v1/settings/household': () => jsonResponse(200, { timezone: 'America/New_York', dinner_time: '17:30' }),
  '/api/v1/settings/ai': () => jsonResponse(200, { provider: 'anthropic', model: 'claude-sonnet-4-5' }),
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
