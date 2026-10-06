import { useState, type ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { Route, Routes } from 'react-router'
import { ApiProvider, createQueryClient, type ApiClient } from './api/index.ts'
import { AuthBootstrap } from './AuthBootstrap.tsx'
import { ONBOARDING_PATH, OnboardingGate } from './onboarding/OnboardingGate.tsx'
import { OnboardingPage } from './onboarding/OnboardingPage.tsx'
import { PlaceholderPage } from './pages/PlaceholderPage.tsx'
import { OfflinePill } from './pwa/OfflinePill.tsx'
import { UpdatePrompt, type UpdateContainer } from './pwa/UpdatePrompt.tsx'
import { AISettingsPage } from './settings/AISettingsPage.tsx'
import { HouseholdSettingsPage } from './settings/HouseholdSettingsPage.tsx'
import { SettingsPage } from './settings/SettingsPage.tsx'
import { AppShell } from './shell/AppShell.tsx'

// Every route the app serves, with the heading its placeholder page shows.
export const routes = [
  { path: '/', title: 'Home' },
  { path: '/onboarding', title: 'Onboarding' },
  { path: '/plan', title: 'Plan' },
  { path: '/plan/shopping', title: 'Supply run' },
  { path: '/plan/shopping/archive', title: 'Supply archive' },
  { path: '/discover', title: 'Discover' },
  { path: '/quick', title: 'Quick dinners' },
  { path: '/cook-from-pantry', title: 'Cook from what I have' },
  { path: '/dinner/:id', title: 'Dinner' },
  { path: '/settings', title: 'Settings' },
  { path: '/settings/notifications', title: 'Notifications' },
  { path: '/settings/ai', title: 'AI assistant' },
  { path: '/settings/household', title: 'Household' },
  { path: '/settings/regulars', title: 'Regulars' },
  { path: '/settings/calendar', title: 'Calendar' },
] as const

// The screens built so far; every other route shows its placeholder page.
const screens: Partial<Record<(typeof routes)[number]['path'], ReactNode>> = {
  '/settings': <SettingsPage />,
  '/settings/ai': <AISettingsPage />,
  '/settings/household': <HouseholdSettingsPage />,
}

// AppRoutes is the shell and its pages, shown once /api/v1/me has loaded.
// Onboarding is a full screen of its own, outside the shell; until it is
// finished, OnboardingGate sends every other route to it.
export function AppRoutes() {
  return (
    <Routes>
      <Route element={<OnboardingGate />}>
        <Route path={`${ONBOARDING_PATH}/*`} element={<OnboardingPage />} />
        <Route element={<AppShell />}>
          {routes
            .filter(({ path }) => path !== ONBOARDING_PATH)
            .map(({ path, title }) => (
              <Route key={path} path={path} element={screens[path] ?? <PlaceholderPage title={title} />} />
            ))}
        </Route>
      </Route>
    </Routes>
  )
}

export type AppProps = {
  api: ApiClient
  // navigator.serviceWorker, or undefined where there is none (and in tests).
  serviceWorker: UpdateContainer | undefined
}

function reloadPage() {
  window.location.reload()
}

// App expects to be rendered by a data router's splat route
// (createBrowserRouter in main.tsx, createMemoryRouter in tests), so pages can
// block navigation.
export default function App({ api, serviceWorker }: AppProps) {
  const [queryClient] = useState(createQueryClient)
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <AuthBootstrap>
          <AppRoutes />
        </AuthBootstrap>
        <OfflinePill />
        <UpdatePrompt container={serviceWorker} reload={reloadPage} />
      </QueryClientProvider>
    </ApiProvider>
  )
}
