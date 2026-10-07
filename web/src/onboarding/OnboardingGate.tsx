import { useEffect } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router'
import { useApi } from '../api/index.ts'
import { LoadingScreen } from '../AuthBootstrap.tsx'
import { useHouseholdSettings } from '../settings/api.ts'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import styles from '../pages/FullScreen.module.css'

export const ONBOARDING_PATH = '/onboarding'

function inOnboarding(pathname: string): boolean {
  return pathname === ONBOARDING_PATH || pathname.startsWith(`${ONBOARDING_PATH}/`)
}

// OnboardingGate reads the household settings before any page: while
// onboarding_completed_at is null, every route outside /onboarding
// redirects there.
//
// The service worker never caches the settings, so offline on the saved
// pages (see AuthBootstrap) they cannot be read: the saved pages open as they
// are, since onboarding needs the server anyway, and the settings are read
// again when the browser is back online. The read runs even while the
// browser reports offline, so that case fails fast instead of waiting.
export function OnboardingGate() {
  const api = useApi()
  const { pathname } = useLocation()
  const settings = useHouseholdSettings()
  const offline = settings.isError && settings.error instanceof TypeError && api.session.verified()
  const { refetch } = settings

  useEffect(() => {
    if (!offline) return
    return api.network.subscribe(() => {
      if (!api.network.offline()) void refetch()
    })
  }, [offline, api, refetch])

  if (settings.data !== undefined) {
    if (settings.data.onboarding_completed_at === null && !inOnboarding(pathname)) {
      return <Navigate to={ONBOARDING_PATH} replace />
    }
    return <Outlet />
  }
  if (offline) return <Outlet />
  if (settings.isError) {
    return (
      <main className={styles.screen}>
        <ErrorBanner onRetry={() => void refetch()}>MealQuest could not load: {settings.error.message}</ErrorBanner>
      </main>
    )
  }
  return <LoadingScreen />
}
