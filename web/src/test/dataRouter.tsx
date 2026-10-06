import { useState, type ReactNode } from 'react'
import { createMemoryRouter, RouterProvider } from 'react-router'

// InDataRouter renders children under a memory data router's splat route at
// path, as main.tsx renders App, so pages that block navigation (useBlocker)
// work in tests. The router is created once, on first render.
export function InDataRouter({ path, children }: { path: string; children: ReactNode }) {
  const [router] = useState(() => createMemoryRouter([{ path: '*', element: children }], { initialEntries: [path] }))
  return <RouterProvider router={router} />
}
