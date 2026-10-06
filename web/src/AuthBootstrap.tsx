import { useEffect, useSyncExternalStore, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ApiError, ME_PATH, useApi } from './api/index.ts'
import { NotInHouseholdPage } from './pages/NotInHouseholdPage.tsx'
import { ErrorBanner } from './ui/ErrorBanner.tsx'
import { Skeleton } from './ui/Skeleton.tsx'
import styles from './pages/FullScreen.module.css'

export const meQueryKey = [ME_PATH] as const

// AuthBootstrap loads GET /api/v1/me before rendering children. A 401 is
// already navigating to sign-in, so the skeleton stays up; an account outside
// the household sees only the "not in this household" screen.
//
// When /me cannot reach the server and it last answered with a member on this
// device, the saved pages open read-only: the service worker answers their
// reads from its cache, and every write is refused for want of a CSRF token.
// /me runs even while the browser reports offline, so that case fails fast
// instead of waiting, and it is fetched again when the browser is back online.
export function AuthBootstrap({ children }: { children: ReactNode }) {
  const api = useApi()
  const forbidden = useSyncExternalStore(api.subscribe, api.forbidden)
  const me = useQuery({ queryKey: meQueryKey, queryFn: api.loadMe, staleTime: Infinity, networkMode: 'always' })
  // fetch rejects with a TypeError only when the network fails.
  const savedPages = me.isError && me.error instanceof TypeError && api.session.verified()
  const { refetch } = me

  useEffect(() => {
    if (!savedPages) return
    return api.network.subscribe(() => {
      if (!api.network.offline()) void refetch()
    })
  }, [savedPages, api, refetch])

  if (forbidden) return <NotInHouseholdPage />
  if (me.isSuccess || savedPages) return children
  if (me.isError && !(me.error instanceof ApiError && me.error.status === 401)) {
    return (
      <main className={styles.screen}>
        <ErrorBanner onRetry={() => void me.refetch()}>MealQuest could not load: {me.error.message}</ErrorBanner>
      </main>
    )
  }
  return <LoadingScreen />
}

// LoadingScreen is the full-screen skeleton shown while MealQuest loads.
export function LoadingScreen() {
  return (
    <main className={styles.screen} aria-busy="true" aria-label="Loading MealQuest">
      <div className={styles.loading}>
        <Skeleton className={styles.title} />
        <Skeleton />
        <Skeleton />
        <Skeleton />
      </div>
    </main>
  )
}
