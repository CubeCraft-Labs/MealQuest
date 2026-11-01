import { Outlet } from 'react-router'
import { BottomBar } from './BottomBar.tsx'
import { Rail } from './Rail.tsx'
import { Sidebar } from './Sidebar.tsx'
import { useBreakpoint, useOrientation } from './useBreakpoint.ts'
import styles from './AppShell.module.css'

// The responsive frame around every page: one nav layout per breakpoint and a
// scrolling main area for the routed page. A portrait viewport is marked so
// the narrower upright tablet keeps its content clear of the rail.
export function AppShell() {
  const breakpoint = useBreakpoint()
  const orientation = useOrientation()
  return (
    <div className={[styles.shell, styles[breakpoint], orientation === 'portrait' && styles.portrait].filter(Boolean).join(' ')}>
      {breakpoint === 'mobile' && <BottomBar />}
      {breakpoint === 'tablet' && <Rail />}
      {breakpoint === 'desktop' && <Sidebar />}
      <main className={styles.main}>
        <Outlet />
      </main>
    </div>
  )
}
