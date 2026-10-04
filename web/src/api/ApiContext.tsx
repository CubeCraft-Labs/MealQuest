import { createContext, useContext, type ReactNode } from 'react'
import type { ApiClient } from './client.ts'

const ApiContext = createContext<ApiClient | null>(null)

export function ApiProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>
}

// useApi returns the app's API client. It must be rendered inside ApiProvider.
export function useApi(): ApiClient {
  const client = useContext(ApiContext)
  if (client === null) throw new Error('useApi: no ApiProvider above this component')
  return client
}
