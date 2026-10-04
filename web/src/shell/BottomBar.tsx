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

// The active item and every item's layout offset within the bar when the pill
// was last placed (offsets ignore transforms, so a running FLIP does not skew
// them).
type Geometry = {
  active: HTMLElement | null
  itemLefts: number[]
}

// Mobile nav: a dark pill bar floating over the bottom of the screen. Inactive
// items show only their icon; the active item adds its label. One shared
// grape pill sits behind the items and slides to the active one.
export function BottomBar() {
  const barRef = useRef<HTMLElement>(null)
  const pillRef = useRef<HTMLSpanElement>(null)
  const placed = useRef<Geometry | null>(null)
  // The pill slide and item FLIPs currently running.
  const sliding = useRef<Animation[]>([])
  const stopSlide = () => {
    sliding.current.forEach((animation) => animation.cancel())
    sliding.current = []
  }
  const { pathname } = useLocation()

  // After each navigation: if the active item changed, move the pill to it and,
  // motion allowed, slide the pill, FLIP the items and fade the label in. A
  // slide still running is measured where it is on screen, then stopped, so
  // the next one starts from there instead of from its destination.
  useLayoutEffect(() => {
    const bar = barRef.current!
    const pill = pillRef.current!
    const active = activeItem(bar)
    const before = placed.current
    if (before && before.active === active) return
    const from = before && onScreen(bar, pill, before)
    stopSlide()
    placed.current = place(bar, pill, active)
    if (!before || !from || !active || window.matchMedia(REDUCED_MOTION_QUERY).matches) return

    const after = placed.current
    if (before.active) {
      sliding.current.push(
        pill.animate(
          [
            { left: `${from.pill.left}px`, width: `${from.pill.width}px` },
            { left: `${active.offsetLeft}px`, width: `${active.offsetWidth}px` },
          ],
          { duration: SLIDE_MS, easing: SPRING },
        ),
      )
    }
    items(bar).forEach((item, i) => {
      const dx = from.itemLefts[i]! - after.itemLefts[i]!
      if (dx === 0) return
      sliding.current.push(
        item.animate([{ transform: `translateX(${dx}px)` }, { transform: 'translateX(0)' }], {
          duration: SLIDE_MS,
          easing: SPRING,
        }),
      )
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
  // stop any running slide and re-place the pill (without animating) on both. A failed font load
  // rejects fonts.ready and is left to surface as an unhandled rejection.
  useLayoutEffect(() => {
    const bar = barRef.current!
    const pill = pillRef.current!
    let mounted = true
    const replace = () => {
      if (!mounted) return
      stopSlide()
      placed.current = place(bar, pill, activeItem(bar))
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

// Where the pill and items are on screen right now, as offsets within the
// bar. Bounding boxes include a running slide: the pill's box is its animated
// left and width, and an item's box is its current layout plus whatever FLIP
// translation remains, which applies to the layout it had when last placed.
function onScreen(bar: HTMLElement, pill: HTMLElement, before: Geometry) {
  const origin = bar.getBoundingClientRect().left + bar.clientLeft
  const pillBox = pill.getBoundingClientRect()
  return {
    pill: { left: pillBox.left - origin, width: pillBox.width },
    itemLefts: items(bar).map(
      (item, i) => before.itemLefts[i]! + item.getBoundingClientRect().left - origin - item.offsetLeft,
    ),
  }
}

// Puts the pill on the active item (hidden when no item is active) and
// returns the resulting geometry.
function place(bar: HTMLElement, pill: HTMLElement, active: HTMLElement | null): Geometry {
  pill.hidden = !active
  pill.style.left = `${active?.offsetLeft ?? 0}px`
  pill.style.width = `${active?.offsetWidth ?? 0}px`
  return { active, itemLefts: items(bar).map((item) => item.offsetLeft) }
}
