import { useCallback, useState, useSyncExternalStore } from 'react'

export type Breakpoint = 'mobile' | 'tablet' | 'desktop'

// The handoff's three layouts: mobile < 768 px, tablet 768–1199 px,
// desktop ≥ 1200 px.
export const TABLET_QUERY = '(min-width: 768px)'
export const DESKTOP_QUERY = '(min-width: 1200px)'

// The layout the current viewport calls for, updated as the viewport crosses
// a breakpoint.
export function useBreakpoint(): Breakpoint {
  const [lists] = useState(() => ({
    tablet: window.matchMedia(TABLET_QUERY),
    desktop: window.matchMedia(DESKTOP_QUERY),
  }))
  const subscribe = useCallback(
    (onChange: () => void) => {
      lists.tablet.addEventListener('change', onChange)
      lists.desktop.addEventListener('change', onChange)
      return () => {
        lists.tablet.removeEventListener('change', onChange)
        lists.desktop.removeEventListener('change', onChange)
      }
    },
    [lists],
  )
  return useSyncExternalStore(subscribe, () =>
    lists.desktop.matches ? 'desktop' : lists.tablet.matches ? 'tablet' : 'mobile',
  )
}
