import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { AppRoutes } from '../App.tsx'
import { ApiProviders, SETTINGS_API } from '../test/ApiProviders.tsx'
import { declaredStyle } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { stubFetchByPath } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import styles from './BottomBar.module.css'

const MOBILE = 390
const SPRING = 'cubic-bezier(.34, 1.4, .64, 1)'

// jsdom does no layout, so lay the bar out here: items sit side by side from
// 6 px in, inactive items 48 px wide and the active one (icon + label) 110 px.
function layOut(element: HTMLElement): { left: number; width: number } {
  const bar = element.parentElement!
  const items = Array.from(bar.querySelectorAll<HTMLElement>(`.${styles.item}`))
  let left = 6
  for (const item of items) {
    const width = item.getAttribute('aria-current') === 'page' ? 110 : 48
    if (item === element) return { left, width }
    left += width
  }
  return { left: 0, width: 0 }
}

// jsdom paints nothing either, so bounding boxes come from the laid-out
// offsets with the bar at the viewport's left edge. A test mid-slide sets
// where the pill is drawn and how far each item is still translated.
let drawnPill: { left: number; width: number } | null
let translated: Map<Element, number>

function box(left: number, width: number): DOMRect {
  return { left, width, x: left, right: left + width, top: 0, y: 0, bottom: 0, height: 0 } as DOMRect
}

let animate: Mock<Element['animate']>

beforeEach(() => {
  drawnPill = null
  translated = new Map()
  // jsdom has no Web Animations API.
  animate = vi.fn<Element['animate']>(() => ({ cancel: vi.fn() }) as unknown as Animation)
  Element.prototype.animate = animate
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    if (!(this instanceof HTMLElement)) return box(0, 0)
    if (this.classList.contains(styles.pill!)) {
      return drawnPill
        ? box(drawnPill.left, drawnPill.width)
        : box(parseFloat(this.style.left), parseFloat(this.style.width))
    }
    if (this.classList.contains(styles.item!)) {
      return box(this.offsetLeft + (translated.get(this) ?? 0), this.offsetWidth)
    }
    return box(0, 0)
  })
  vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockImplementation(function (this: HTMLElement) {
    return layOut(this).left
  })
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (this: HTMLElement) {
    return layOut(this).width
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  Reflect.deleteProperty(Element.prototype, 'animate')
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

async function renderAt(path: string, reducedMotion = false) {
  mockViewport(MOBILE, { reducedMotion })
  stubFetchByPath(SETTINGS_API)
  render(
    <InDataRouter path={path}>
      <ApiProviders>
        <AppRoutes />
      </ApiProviders>
    </InDataRouter>,
  )
  // The routes wait for the household settings (OnboardingGate).
  await screen.findByRole('navigation', { name: 'Primary' })
}

function nav() {
  return screen.getByRole('navigation', { name: 'Primary' })
}

function pill(): HTMLElement {
  return nav().querySelector<HTMLElement>(`.${styles.pill}`)!
}

function link(name: string): HTMLElement {
  return within(nav()).getByRole('link', { name })
}

function runningOn(element: Element): Animation[] {
  return animate.mock.contexts
    .map((target, i) => ({ target, animation: animate.mock.results[i]!.value as Animation }))
    .filter(({ target }) => target === element)
    .map(({ animation }) => animation)
}

function animationsOf(element: Element) {
  return animate.mock.contexts
    .map((target, i) => ({ target, args: animate.mock.calls[i]! }))
    .filter(({ target }) => target === element)
    .map(({ args }) => ({ keyframes: args[0], options: args[1] as KeyframeAnimationOptions }))
}

describe('BottomBar', () => {
  it('places the pill on the active item without animating on first render', async () => {
    await renderAt('/plan')
    expect(pill().style.left).toBe('54px')
    expect(pill().style.width).toBe('110px')
    expect(pill()).toBeVisible()
    expect(animate).not.toHaveBeenCalled()
  })

  it('hides the pill when no item is active', async () => {
    await renderAt('/cook-from-pantry')
    expect(pill()).not.toBeVisible()
  })

  it('slides the pill from the old item to the new one over 420 ms on the spring curve', async () => {
    await renderAt('/')
    fireEvent.click(link('Quick'))

    expect(animationsOf(pill())).toEqual([
      {
        keyframes: [
          { left: '6px', width: '110px' },
          { left: '150px', width: '110px' },
        ],
        options: { duration: 420, easing: SPRING },
      },
    ])
    expect(pill().style.left).toBe('150px')
    expect(pill().style.width).toBe('110px')
  })

  it('FLIPs every item that moved from its old left to its new one with the same timing', async () => {
    await renderAt('/')
    fireEvent.click(link('Quick'))

    // Home shrinks from 110 to 48 px, so Plan and Discover shift 62 px left
    // and Quick grows in place; Settings ends where it started.
    for (const name of ['Plan', 'Discover']) {
      expect(animationsOf(link(name))).toEqual([
        {
          keyframes: [{ transform: 'translateX(62px)' }, { transform: 'translateX(0)' }],
          options: { duration: 420, easing: SPRING },
        },
      ])
    }
    expect(animationsOf(link('Quick'))).toEqual([
      {
        keyframes: [{ transform: 'translateX(62px)' }, { transform: 'translateX(0)' }],
        options: { duration: 420, easing: SPRING },
      },
    ])
    expect(animationsOf(link('Home'))).toEqual([])
    expect(animationsOf(link('Settings'))).toEqual([])
  })

  it('fades the new label in over 220 ms ease-out after 120 ms', async () => {
    await renderAt('/')
    fireEvent.click(link('Discover'))

    const label = within(link('Discover')).getByText('Discover')
    expect(label).toHaveClass(styles.label!)
    expect(animationsOf(label)).toEqual([
      {
        keyframes: [
          { opacity: 0, transform: 'translateX(-4px)' },
          { opacity: 1, transform: 'none' },
        ],
        options: { duration: 220, delay: 120, easing: 'ease-out', fill: 'backwards' },
      },
    ])
  })

  it('rings a focused item in on-accent ink, which shows on the dark bar', async () => {
    await renderAt('/')
    const item = link('Plan')
    // A key press makes the next focus a keyboard one, which :focus-visible
    // matches; earlier tests' clicks would leave it a pointer one.
    fireEvent.keyDown(document.body, { key: 'Tab' })
    item.focus()
    expect(item).toHaveFocus()
    expect(item.matches(':focus-visible')).toBe(true)
    expect(declaredStyle(item).getPropertyValue('outline-color')).toBe('var(--on-accent)')
  })

  it('does not animate when the active item stays the same', async () => {
    await renderAt('/plan')
    fireEvent.click(link('Plan'))
    expect(animate).not.toHaveBeenCalled()
  })

  it('jumps the pill to the new item with no animation under reduced motion', async () => {
    await renderAt('/', true)
    fireEvent.click(link('Settings'))

    expect(link('Settings')).toHaveAttribute('aria-current', 'page')
    expect(animate).not.toHaveBeenCalled()
    expect(pill().style.left).toBe('198px')
    expect(pill().style.width).toBe('110px')
  })

  it('re-places the pill when the viewport resizes', async () => {
    await renderAt('/discover')
    expect(pill().style.left).toBe('102px')
    vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockReturnValue(120)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(96)
    fireEvent(window, new Event('resize'))
    expect(pill().style.left).toBe('120px')
    expect(pill().style.width).toBe('96px')
    expect(animate).not.toHaveBeenCalled()
  })

  it('starts a second slide from where the first one is on screen, not from its destination', async () => {
    await renderAt('/')
    fireEvent.click(link('Quick'))
    const [firstPill] = runningOn(pill())
    const firstPlan = runningOn(link('Plan'))[0]!

    // Midway: the pill is drawn at 78 px on its way from 6 to 150, and Plan
    // still carries 31 of its 62 px FLIP translation.
    drawnPill = { left: 78, width: 110 }
    translated.set(link('Plan'), 31)
    animate.mockClear()
    fireEvent.click(link('Settings'))

    expect(firstPill!.cancel).toHaveBeenCalled()
    expect(firstPlan.cancel).toHaveBeenCalled()
    expect(animationsOf(pill())).toEqual([
      {
        keyframes: [
          { left: '78px', width: '110px' },
          { left: '198px', width: '110px' },
        ],
        options: { duration: 420, easing: SPRING },
      },
    ])
    // Plan's layout does not move between Quick and Settings, so it only
    // finishes the 31 px it had left. Settings moves 62 px left.
    expect(animationsOf(link('Plan'))).toEqual([
      {
        keyframes: [{ transform: 'translateX(31px)' }, { transform: 'translateX(0)' }],
        options: { duration: 420, easing: SPRING },
      },
    ])
    expect(animationsOf(link('Settings'))).toEqual([
      {
        keyframes: [{ transform: 'translateX(62px)' }, { transform: 'translateX(0)' }],
        options: { duration: 420, easing: SPRING },
      },
    ])
  })

  it('stops a running slide and places the pill on the active item when the viewport resizes', async () => {
    await renderAt('/')
    fireEvent.click(link('Quick'))
    const slide = [pill(), link('Plan'), link('Discover'), link('Quick')].flatMap(runningOn)
    expect(slide).toHaveLength(4)

    animate.mockClear()
    vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockReturnValue(140)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(96)
    fireEvent(window, new Event('resize'))

    for (const animation of slide) expect(animation.cancel).toHaveBeenCalled()
    expect(animate).not.toHaveBeenCalled()
    expect(pill().style.left).toBe('140px')
    expect(pill().style.width).toBe('96px')
  })

  it('re-places the pill once the web fonts are ready', async () => {
    let fontsReady!: () => void
    const ready = new Promise<void>((resolve) => (fontsReady = resolve))
    Object.defineProperty(document, 'fonts', { configurable: true, value: { ready } })
    try {
      await renderAt('/discover')
      vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(124)
      fontsReady()
      await ready
      expect(pill().style.width).toBe('124px')
    } finally {
      Reflect.deleteProperty(document, 'fonts')
    }
  })

  it('crossfades the icon colour with a .2s colour transition', async () => {
    await renderAt('/')
    for (const item of within(nav()).getAllByRole('link')) {
      expect(declaredStyle(item).transition).toBe('color .2s')
    }
  })
})
