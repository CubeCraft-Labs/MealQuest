import { Outlet } from 'react-router'
import { BottomBar } from './BottomBar.tsx'
import { Rail } from './Rail.tsx'
import { Sidebar } from './Sidebar.tsx'
import { useBreakpoint } from './useBreakpoint.ts'
import styles from './AppShell.module.css'

// The responsive frame around every page: one nav layout per breakpoint and a
// scrolling main area for the routed page.
export function AppShell() {
  const breakpoint = useBreakpoint()
  return (
    <div className={`${styles.shell} ${styles[breakpoint]}`}>
      {breakpoint === 'mobile' && <BottomBar />}
      {breakpoint === 'tablet' && <Rail />}
      {breakpoint === 'desktop' && <Sidebar />}
      <main className={styles.main}>
        <Outlet />
      </main>
    </div>
  )
}
