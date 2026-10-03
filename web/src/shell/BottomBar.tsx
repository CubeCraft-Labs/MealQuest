import { useLayoutEffect, useRef } from 'react'
import { NavLink, useLocation } from 'react-router'
import { Icon } from '../ui/Icon.tsx'
import { navItems } from './navItems.ts'
import styles from './BottomBar.module.css'

// The handoff's Motion §1 timing. SPRING is the --ease-spring token's value.
const SLIDE_MS = 420
const SPRING = 'cubic-bezier(.34, 1.4, .64, 1)'
const LABEL_MS = 220
const LABEL_DELAY_MS = 120
const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

// Where the pill and every item sat when the pill was last placed, as layout
// offsets within the bar (offsets ignore transforms, so a running FLIP does
// not skew them).
type Geometry = {
  active: HTMLElement | null
  pill: { left: number; width: number }
  itemLefts: number[]
}

// Mobile nav: a dark pill bar floating over the bottom of the screen. Inactive
// items show only their icon; the active item adds its label. One shared
// grape pill sits behind the items and slides to the active one.
export function BottomBar() {
  const barRef = useRef<HTMLElement>(null)
  const pillRef = useRef<HTMLSpanElement>(null)
  const placed = useRef<Geometry | null>(null)
  const { pathname } = useLocation()

  // After each navigation: if the active item changed, move the pill to it and,
  // motion allowed, slide the pill, FLIP the items and fade the label in.
  useLayoutEffect(() => {
    const bar = barRef.current!
    const pill = pillRef.current!
    const active = activeItem(bar)
    const before = placed.current
    if (before && before.active === active) return
    placed.current = place(bar, pill, active)
    if (!before || !active || window.matchMedia(REDUCED_MOTION_QUERY).matches) return

    const after = placed.current
    if (before.active) {
      pill.animate(
        [
          { left: `${before.pill.left}px`, width: `${before.pill.width}px` },
          { left: `${after.pill.left}px`, width: `${after.pill.width}px` },
        ],
        { duration: SLIDE_MS, easing: SPRING },
      )
    }
    items(bar).forEach((item, i) => {
      const dx = before.itemLefts[i]! - after.itemLefts[i]!
      if (dx === 0) return
      item.animate([{ transform: `translateX(${dx}px)` }, { transform: 'translateX(0)' }], {
        duration: SLIDE_MS,
        easing: SPRING,
      })
    })
    active.querySelector(`.${styles.label}`)!.animate(
      [
        { opacity: 0, transform: 'translateX(-4px)' },
        { opacity: 1, transform: 'none' },
      ],
      { duration: LABEL_MS, delay: LABEL_DELAY_MS, easing: 'ease-out', fill: 'backwards' },
    )
  }, [pathname])

  // Item widths change with the viewport and once the web fonts arrive, so
  // re-place the pill (without animating) on both. A failed font load
  // rejects fonts.ready and is left to surface as an unhandled rejection.
  useLayoutEffect(() => {
    const bar = barRef.current!
    const pill = pillRef.current!
    let mounted = true
    const replace = () => {
      if (mounted) placed.current = place(bar, pill, activeItem(bar))
    }
    window.addEventListener('resize', replace)
    // jsdom has no document.fonts; every browser the app targets does.
    if (document.fonts) void document.fonts.ready.then(replace)
    return () => {
      mounted = false
      window.removeEventListener('resize', replace)
    }
  }, [])

  return (
    <nav ref={barRef} aria-label="Primary" className={styles.bar}>
      <span ref={pillRef} className={styles.pill} aria-hidden="true" />
      {navItems.map((item) => (
        <NavLink key={item.to} to={item.to} end={item.end} className={styles.item}>
          {({ isActive }) => (
            <>
              <Icon icon={item.icon} size="nav" />
              <span className={isActive ? styles.label : styles.hiddenLabel}>{item.label}</span>
            </>
          )}
        </NavLink>
      ))}
    </nav>
  )
}

function items(bar: HTMLElement): HTMLElement[] {
  return Array.from(bar.querySelectorAll<HTMLElement>(`.${styles.item}`))
}

// NavLink's aria-current is the one record of which item is active.
function activeItem(bar: HTMLElement): HTMLElement | null {
  return bar.querySelector<HTMLElement>(`.${styles.item}[aria-current="page"]`)
}

// Puts the pill on the active item (hidden when no item is active) and
// returns the resulting geometry.
function place(bar: HTMLElement, pill: HTMLElement, active: HTMLElement | null): Geometry {
  const geometry = {
    active,
    pill: { left: active?.offsetLeft ?? 0, width: active?.offsetWidth ?? 0 },
    itemLefts: items(bar).map((item) => item.offsetLeft),
  }
  pill.hidden = !active
  pill.style.left = `${geometry.pill.left}px`
  pill.style.width = `${geometry.pill.width}px`
  return geometry
}
