import { useSyncExternalStore, type ReactNode } from 'react'
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
export function AuthBootstrap({ children }: { children: ReactNode }) {
  const api = useApi()
  const forbidden = useSyncExternalStore(api.subscribe, api.forbidden)
  const me = useQuery({ queryKey: meQueryKey, queryFn: api.loadMe, staleTime: Infinity })

  if (forbidden) return <NotInHouseholdPage />
  if (me.isSuccess) return children
  if (me.isError && !(me.error instanceof ApiError && me.error.status === 401)) {
    return (
      <main className={styles.screen}>
        <ErrorBanner onRetry={() => void me.refetch()}>MealQuest could not load: {me.error.message}</ErrorBanner>
      </main>
    )
  }
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
