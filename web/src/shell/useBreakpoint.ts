import { useCallback, useState, useSyncExternalStore } from 'react'

export type Breakpoint = 'mobile' | 'tablet' | 'desktop'

// The handoff's three layouts: mobile < 768 px, tablet 768–1199 px,
// desktop ≥ 1200 px.
export const TABLET_QUERY = '(min-width: 768px)'
export const DESKTOP_QUERY = '(min-width: 1200px)'

export type Orientation = 'portrait' | 'landscape'

// A tablet held upright (820 × 1180) has the rail and the tablet layout but
// far less width than one on its side, so pages reflow for it.
export const PORTRAIT_QUERY = '(orientation: portrait)'

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

// The viewport's orientation, updated as the device turns.
export function useOrientation(): Orientation {
  const [list] = useState(() => window.matchMedia(PORTRAIT_QUERY))
  const subscribe = useCallback(
    (onChange: () => void) => {
      list.addEventListener('change', onChange)
      return () => list.removeEventListener('change', onChange)
    },
    [list],
  )
  return useSyncExternalStore(subscribe, () => (list.matches ? 'portrait' : 'landscape'))
}
