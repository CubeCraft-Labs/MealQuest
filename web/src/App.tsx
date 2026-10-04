import { useState } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { Route, Routes } from 'react-router'
import { ApiProvider, createQueryClient, type ApiClient } from './api/index.ts'
import { AuthBootstrap } from './AuthBootstrap.tsx'
import { PlaceholderPage } from './pages/PlaceholderPage.tsx'
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

// AppRoutes is the shell and its pages, shown once /api/v1/me has loaded.
export function AppRoutes() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        {routes.map(({ path, title }) => (
          <Route key={path} path={path} element={<PlaceholderPage title={title} />} />
        ))}
      </Route>
    </Routes>
  )
}

export type AppProps = {
  api: ApiClient
}

// App expects to be rendered inside a router (BrowserRouter in main.tsx,
// MemoryRouter in tests).
export default function App({ api }: AppProps) {
  const [queryClient] = useState(createQueryClient)
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <AuthBootstrap>
          <AppRoutes />
        </AuthBootstrap>
      </QueryClientProvider>
    </ApiProvider>
  )
}
