import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import App from '../App.tsx'
import { declaredStyle } from '../test/cssRules.ts'
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

let animate: Mock<Element['animate']>

beforeEach(() => {
  // jsdom has no Web Animations API.
  animate = vi.fn<Element['animate']>()
  Element.prototype.animate = animate
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
})

function renderAt(path: string, reducedMotion = false) {
  mockViewport(MOBILE, { reducedMotion })
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
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

function animationsOf(element: Element) {
  return animate.mock.contexts
    .map((target, i) => ({ target, args: animate.mock.calls[i]! }))
    .filter(({ target }) => target === element)
    .map(({ args }) => ({ keyframes: args[0], options: args[1] as KeyframeAnimationOptions }))
}

describe('BottomBar', () => {
  it('places the pill on the active item without animating on first render', () => {
    renderAt('/plan')
    expect(pill().style.left).toBe('54px')
    expect(pill().style.width).toBe('110px')
    expect(pill()).toBeVisible()
    expect(animate).not.toHaveBeenCalled()
  })

  it('hides the pill when no item is active', () => {
    renderAt('/onboarding')
    expect(pill()).not.toBeVisible()
  })

  it('slides the pill from the old item to the new one over 420 ms on the spring curve', () => {
    renderAt('/')
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

  it('FLIPs every item that moved from its old left to its new one with the same timing', () => {
    renderAt('/')
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

  it('fades the new label in over 220 ms ease-out after 120 ms', () => {
    renderAt('/')
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

  it('does not animate when the active item stays the same', () => {
    renderAt('/plan')
    fireEvent.click(link('Plan'))
    expect(animate).not.toHaveBeenCalled()
  })

  it('jumps the pill to the new item with no animation under reduced motion', () => {
    renderAt('/', true)
    fireEvent.click(link('Settings'))

    expect(link('Settings')).toHaveAttribute('aria-current', 'page')
    expect(animate).not.toHaveBeenCalled()
    expect(pill().style.left).toBe('198px')
    expect(pill().style.width).toBe('110px')
  })

  it('re-places the pill when the viewport resizes', () => {
    renderAt('/discover')
    expect(pill().style.left).toBe('102px')
    vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockReturnValue(120)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(96)
    fireEvent(window, new Event('resize'))
    expect(pill().style.left).toBe('120px')
    expect(pill().style.width).toBe('96px')
    expect(animate).not.toHaveBeenCalled()
  })

  it('re-places the pill once the web fonts are ready', async () => {
    let fontsReady!: () => void
    const ready = new Promise<void>((resolve) => (fontsReady = resolve))
    Object.defineProperty(document, 'fonts', { configurable: true, value: { ready } })
    try {
      renderAt('/discover')
      vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(124)
      fontsReady()
      await ready
      expect(pill().style.width).toBe('124px')
    } finally {
      Reflect.deleteProperty(document, 'fonts')
    }
  })

  it('crossfades the icon colour with a .2s colour transition', () => {
    renderAt('/')
    for (const item of within(nav()).getAllByRole('link')) {
      expect(declaredStyle(item).transition).toBe('color .2s')
    }
  })
})
