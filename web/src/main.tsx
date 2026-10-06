import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router'
import './styles/tokens.css'
import './styles/base.css'
import App from './App.tsx'
import { createApiClient, createVerifiedSession } from './api/index.ts'
import { createNetworkStatus } from './pwa/networkStatus.ts'

const root = document.getElementById('root')
if (!root) {
  throw new Error('MealQuest: #root element missing from index.html')
}

const api = createApiClient(window.location, createNetworkStatus(window), createVerifiedSession(window.localStorage))

// A data router, so pages can block navigation (useBlocker); App's own
// <Routes> match every path under its splat route.
const router = createBrowserRouter([
  {
    path: '*',
    element: <App api={api} serviceWorker={'serviceWorker' in navigator ? navigator.serviceWorker : undefined} />,
  },
])

createRoot(root).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
