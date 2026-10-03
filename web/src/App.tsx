import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Route, Routes } from 'react-router'

// Placeholder shell only: no product UI, auth or API calls yet.
function Shell() {
  return (
    <main>
      <h1>MealQuest</h1>
    </main>
  )
}

// App expects to be rendered inside a router (BrowserRouter in main.tsx,
// MemoryRouter in tests).
export default function App() {
  const [queryClient] = useState(() => new QueryClient())
  return (
    <QueryClientProvider client={queryClient}>
      <Routes>
        <Route path="*" element={<Shell />} />
      </Routes>
    </QueryClientProvider>
  )
}
