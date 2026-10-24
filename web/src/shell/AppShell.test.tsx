import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AppRoutes } from '../App.tsx'
import { ApiProviders, SETTINGS_API } from '../test/ApiProviders.tsx'
import { declaredStyle, outerSize } from '../test/cssRules.ts'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody } from '../test/dinners.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { jsonResponse, stubFetchByPath } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import bottomBarStyles from './BottomBar.module.css'
import railStyles from './Rail.module.css'
import sidebarStyles from './Sidebar.module.css'

const ORDER = ['Home', 'Plan', 'Discover', 'Quick', 'Settings']
const MOBILE = 390
const TABLET = 1180
const DESKTOP = 1440

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.unstubAllGlobals()
})

async function renderAt(path: string, width: number, api: Record<string, () => Response> = SETTINGS_API) {
  const resize = mockViewport(width)
  stubFetchByPath(api)
  render(
    <InDataRouter path={path}>
      <ApiProviders>
        <AppRoutes />
      </ApiProviders>
    </InDataRouter>,
  )
  // The routes wait for the household settings (OnboardingGate).
  await screen.findByRole('navigation', { name: 'Primary' })
  return resize
}

function nav() {
  return screen.getByRole('navigation', { name: 'Primary' })
}

function navLinks() {
  return within(nav()).getAllByRole('link')
}

// The px the mobile scroll area reserves beneath its content, above the
// safe area.
function reservedBelowMain(): number {
  const padding = declaredStyle(screen.getByRole('main')).paddingBottom
  const reserved = /^calc\((\d+)px \+ env\(safe-area-inset-bottom\)\)$/.exec(padding)
  expect(reserved, `padding-bottom ${JSON.stringify(padding)}`).not.toBeNull()
  return Number(reserved![1])
}

// The px the bottom bar covers at the bottom of the screen, above the safe
// area: its height plus its offset from the bottom.
function coveredByBar(): number {
  const bottom = /^calc\((\d+)px \+ env\(safe-area-inset-bottom\)\)$/.exec(declaredStyle(nav()).bottom)
  expect(bottom).not.toBeNull()
  return outerSize(nav(), 'height') + Number(bottom![1])
}

function current() {
  return navLinks()
    .filter((link) => link.getAttribute('aria-current') === 'page')
    .map((link) => link.textContent)
}

describe('AppShell', () => {
  it.each([MOBILE, TABLET, DESKTOP])('orders the nav Home · Plan · Discover · Quick · Settings at %i px', async (width) => {
    await renderAt('/', width)
    expect(navLinks().map((link) => link.textContent)).toEqual(ORDER)
    expect(navLinks().map((link) => link.getAttribute('href'))).toEqual([
      '/',
      '/plan',
      '/discover',
      '/quick',
      '/settings',
    ])
  })

  it('shows only the mobile bottom bar below 768 px', async () => {
    await renderAt('/', 767)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
    expect(nav()).toHaveClass(bottomBarStyles.bar!)
    expect(screen.queryByText('M')).not.toBeInTheDocument()
    expect(screen.queryByText('MealQuest')).not.toBeInTheDocument()
    expect(screen.queryByText('Our kitchen')).not.toBeInTheDocument()

    const bar = declaredStyle(nav())
    expect(bar.position).toBe('absolute')
    expect(bar.left).toBe('12px')
    expect(bar.right).toBe('12px')
    expect(bar.bottom).toBe('calc(16px + env(safe-area-inset-bottom))')
    expect(bar.height).toBe('62px')
    expect(outerSize(nav(), 'height')).toBe(62)
    expect(bar.background).toBe('var(--color-text)')
    expect(bar.borderRadius).toBe('var(--radius-pill)')
  })

  it('shows the active mobile item as icon and label over the one grape pill, inactive items as neutral icons', async () => {
    await renderAt('/discover', MOBILE)
    const pills = nav().querySelectorAll(`.${bottomBarStyles.pill}`)
    expect(pills).toHaveLength(1)
    const pill = declaredStyle(pills[0]!)
    expect(pill.position).toBe('absolute')
    expect(pill.top).toBe('10px')
    expect(pill.height).toBe('42px')
    expect(pill.background).toBe('var(--color-accent)')
    expect(Number(pill.zIndex)).toBeLessThan(Number(declaredStyle(navLinks()[0]!).zIndex))

    const active = screen.getByRole('link', { name: 'Discover' })
    expect(declaredStyle(active).color).toBe('var(--on-accent)')
    expect(within(active).getByText('Discover')).toHaveClass(bottomBarStyles.label!)
    expect(declaredStyle(within(active).getByText('Discover')).fontSize).toBe('13px')

    const inactive = screen.getByRole('link', { name: 'Plan' })
    expect(declaredStyle(inactive).color).toBe('var(--color-neutral-400)')
    expect(within(inactive).getByText('Plan')).toHaveClass(bottomBarStyles.hiddenLabel!)
  })

  it('makes the full 62 px bar height the mobile tap target', async () => {
    await renderAt('/', MOBILE)
    for (const link of navLinks()) {
      expect(declaredStyle(link).height).toBe('62px')
      expectMinHitArea(link)
    }
  })

  it('reserves at least 100 px below the mobile scroll area', async () => {
    await renderAt('/', MOBILE)
    expect(reservedBelowMain()).toBeGreaterThanOrEqual(100)
    expect(reservedBelowMain()).toBeGreaterThanOrEqual(coveredByBar())
  })

  // The bar comes before the scroll area in the document, so without a
  // stacking order of its own anything in a page that forms a stacking
  // context (a Photo's filter, a pending row's opacity) paints over it.
  it('paints the mobile bottom bar above the page content', async () => {
    await renderAt('/', MOBILE)
    const barZ = Number(declaredStyle(nav()).zIndex)
    expect(Number.isInteger(barZ) && barZ > 0, `bar z-index ${JSON.stringify(declaredStyle(nav()).zIndex)}`).toBe(true)
    // The scroll area is one stacking context, so no z-index inside a page
    // can lift its content past the bar.
    expect(declaredStyle(screen.getByRole('main')).getPropertyValue('isolation')).toBe('isolate')
    // Inside the bar, its items still paint above the grape pill.
    const pill = nav().querySelector<HTMLElement>(`.${bottomBarStyles.pill}`)!
    for (const link of navLinks()) {
      expect(Number(declaredStyle(link).zIndex)).toBeGreaterThan(Number(declaredStyle(pill).zIndex))
    }
  })

  it('keeps the last of a long Regulars list reachable above the mobile bottom bar', async () => {
    const regulars = Array.from({ length: 40 }, (_, i) =>
      dinnerBody(`d${i}`, `Regular ${i}`, { method: 'stovetop', minutes_total: 20 }),
    )
    await renderAt('/settings/regulars', MOBILE, {
      ...SETTINGS_API,
      '/api/v1/dinners?status=regular': () => jsonResponse(200, { dinners: regulars }),
    })
    const rows = within(await screen.findByRole('list', { name: 'Regulars' })).getAllByRole('listitem')
    expect(rows).toHaveLength(40)
    const last = rows.at(-1)!
    expect(last).toHaveTextContent('Regular 39')

    // The shell's scroll area is the one scroll container between the last
    // row and the screen, and nothing on the way is pinned, so scrolling it
    // to its end brings the row up into the space it reserves above the bar.
    const main = screen.getByRole('main')
    expect(declaredStyle(main).overflowY).toBe('auto')
    for (let el = last.parentElement; el !== main; el = el!.parentElement) {
      expect(el, 'the last row sits inside the scroll area').not.toBeNull()
      const style = declaredStyle(el!)
      expect(['auto', 'scroll', 'hidden']).not.toContain(style.overflow)
      expect(['auto', 'scroll', 'hidden']).not.toContain(style.overflowY)
      expect(['sticky', 'fixed']).not.toContain(style.position)
    }
    expect(reservedBelowMain()).toBeGreaterThanOrEqual(coveredByBar())
  })

  it.each([768, TABLET, 1199])('shows only the tablet rail with the "M" mark at %i px', async (width) => {
    await renderAt('/plan', width)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
    expect(nav()).toHaveClass(railStyles.rail!)
    expect(declaredStyle(nav()).width).toBe('96px')
    expect(outerSize(nav(), 'width')).toBe(96)
    expect(declaredStyle(nav()).background).toBe('var(--color-surface)')
    const mark = within(nav()).getByText('M')
    expect(declaredStyle(mark).width).toBe('48px')
    expect(outerSize(mark, 'width')).toBe(48)
    expect(outerSize(mark, 'height')).toBe(48)
    expect(declaredStyle(mark).background).toBe('var(--color-accent)')
    expect(screen.queryByText('MealQuest')).not.toBeInTheDocument()
    expect(screen.queryByText('Our kitchen')).not.toBeInTheDocument()

    for (const link of navLinks()) {
      const pill = link.firstElementChild!
      expect(declaredStyle(pill).width).toBe('56px')
      expect(declaredStyle(pill).height).toBe('36px')
      expect(outerSize(pill, 'width')).toBe(56)
      expect(outerSize(pill, 'height')).toBe(36)
      expect(declaredStyle(link).fontSize).toBe('11px')
      expect(declaredStyle(link).fontWeight).toBe('700')
      expectMinHitArea(link)
    }
    expect(screen.getByRole('link', { name: 'Plan' }).firstElementChild).toHaveClass(railStyles.active!)
    expect(screen.getByRole('link', { name: 'Home' }).firstElementChild).not.toHaveClass(railStyles.active!)
  })

  it.each([1200, DESKTOP])('shows only the desktop sidebar with wordmark and household card at %i px', async (width) => {
    await renderAt('/quick', width)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
    const sidebar = nav().closest('aside')!
    expect(sidebar).toHaveClass(sidebarStyles.sidebar!)
    expect(declaredStyle(sidebar).width).toBe('208px')
    expect(declaredStyle(sidebar).background).toBe('var(--color-surface)')
    expect(declaredStyle(sidebar).padding).toBe('28px 16px')
    // The 16 px side padding sits inside the 208 px, not outside it.
    expect(outerSize(sidebar, 'width')).toBe(208)

    const wordmark = within(sidebar).getByText('MealQuest')
    expect(declaredStyle(wordmark).fontFamily).toBe('var(--font-heading)')
    expect(declaredStyle(wordmark).fontSize).toBe('28px')
    expect(declaredStyle(wordmark).color).toBe('var(--color-accent)')

    expect(within(sidebar).getByText('Our kitchen')).toBeInTheDocument()
    expect(within(sidebar).getByText('Shared household')).toBeInTheDocument()
    expect(screen.queryByText('M')).not.toBeInTheDocument()

    const active = screen.getByRole('link', { name: 'Quick' })
    expect(active).toHaveClass(sidebarStyles.active!)
    expect(declaredStyle(active).background).toBe('var(--color-accent)')
    expect(declaredStyle(active).color).toBe('var(--on-accent)')
    expect(declaredStyle(active).fontSize).toBe('15px')
    expect(screen.getByRole('link', { name: 'Home' })).not.toHaveClass(sidebarStyles.active!)
    // Rows stretch across the sidebar, so only their height needs declaring.
    for (const link of navLinks()) expect(declaredStyle(link).minHeight).toBe('44px')
  })

  it('switches layout when the viewport crosses a breakpoint', async () => {
    const resize = await renderAt('/', MOBILE)
    expect(nav()).toHaveClass(bottomBarStyles.bar!)
    resize(TABLET)
    expect(nav()).toHaveClass(railStyles.rail!)
    resize(DESKTOP)
    expect(nav().closest('aside')).toHaveClass(sidebarStyles.sidebar!)
    resize(MOBILE)
    expect(nav()).toHaveClass(bottomBarStyles.bar!)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
  })

  describe.each([
    ['mobile', MOBILE],
    ['tablet', TABLET],
    ['desktop', DESKTOP],
  ])('aria-current (%s)', (_layout, width) => {
    it.each([
      ['/', ['Home']],
      ['/plan', ['Plan']],
      ['/plan/shopping', ['Plan']],
      ['/plan/shopping/archive', ['Plan']],
      ['/discover', ['Discover']],
      ['/quick', ['Quick']],
      ['/settings', ['Settings']],
      ['/settings/notifications', ['Settings']],
      ['/settings/ai', ['Settings']],
      ['/settings/household', ['Settings']],
      ['/settings/regulars', ['Settings']],
      ['/settings/calendar', ['Settings']],
      ['/cook-from-pantry', []],
      ['/dinner/abc123', []],
    ])('%s marks %j current', async (path, expected) => {
      await renderAt(path, width)
      expect(current()).toEqual(expected)
    })
  })
})
