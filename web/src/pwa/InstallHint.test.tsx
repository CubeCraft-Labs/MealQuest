import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DISMISSED_KEY, InstallHint } from './InstallHint.tsx'

const IPHONE_SAFARI =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1'
const IPAD_SAFARI =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15'
const MAC_SAFARI = IPAD_SAFARI
const IPHONE_CHROME =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/138.0.7204.119 Mobile/15E148 Safari/604.1'
const ANDROID_CHROME =
  'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Mobile Safari/537.36'
const DESKTOP_CHROME =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36'

const copy = 'Tap Share, then Add to Home Screen to get notifications'

type Device = {
  userAgent: string
  maxTouchPoints?: number
  // display-mode: standalone
  displayStandalone?: boolean
  // navigator.standalone (Safari only; undefined elsewhere)
  navigatorStandalone?: boolean
}

// mockDevice pretends to be a browser with the given user agent and
// display mode; jsdom has no matchMedia, navigator.maxTouchPoints or
// navigator.standalone.
function mockDevice({ userAgent, maxTouchPoints = 0, displayStandalone = false, navigatorStandalone }: Device) {
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(userAgent)
  Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: maxTouchPoints })
  Object.defineProperty(navigator, 'standalone', { configurable: true, value: navigatorStandalone })
  vi.stubGlobal('matchMedia', (query: string) => {
    if (query !== '(display-mode: standalone)') throw new Error(`unexpected media query ${query}`)
    return { matches: displayStandalone, media: query } as MediaQueryList
  })
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(navigator, 'maxTouchPoints')
  Reflect.deleteProperty(navigator, 'standalone')
  localStorage.clear()
})

describe('InstallHint', () => {
  it('shows on iPhone Safari in a browser tab', () => {
    mockDevice({ userAgent: IPHONE_SAFARI, maxTouchPoints: 5, navigatorStandalone: false })
    render(<InstallHint />)
    expect(screen.getByRole('note')).toHaveTextContent(copy)
  })

  it('shows on iPadOS Safari, which reports itself as a Mac with a touch screen', () => {
    mockDevice({ userAgent: IPAD_SAFARI, maxTouchPoints: 5, navigatorStandalone: false })
    render(<InstallHint />)
    expect(screen.getByRole('note')).toHaveTextContent(copy)
  })

  it('is hidden when navigator.standalone is true', () => {
    mockDevice({ userAgent: IPHONE_SAFARI, maxTouchPoints: 5, navigatorStandalone: true })
    render(<InstallHint />)
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('is hidden when display-mode is standalone', () => {
    mockDevice({ userAgent: IPHONE_SAFARI, maxTouchPoints: 5, displayStandalone: true })
    render(<InstallHint />)
    expect(screen.queryByRole('note')).toBeNull()
  })

  it.each([
    ['Android Chrome', { userAgent: ANDROID_CHROME, maxTouchPoints: 5 }],
    ['desktop Chrome', { userAgent: DESKTOP_CHROME }],
    ['desktop Safari on a Mac', { userAgent: MAC_SAFARI }],
    ['Chrome on iPhone', { userAgent: IPHONE_CHROME, maxTouchPoints: 5 }],
  ])('is hidden on %s', (_name, device: Device) => {
    mockDevice(device)
    render(<InstallHint />)
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('stays dismissed on this device', () => {
    mockDevice({ userAgent: IPHONE_SAFARI, maxTouchPoints: 5, navigatorStandalone: false })
    const first = render(<InstallHint />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('note')).toBeNull()
    expect(localStorage.getItem(DISMISSED_KEY)).toBe('1')

    first.unmount()
    render(<InstallHint />)
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('shows and can be dismissed when storage is unavailable', () => {
    mockDevice({ userAgent: IPHONE_SAFARI, maxTouchPoints: 5, navigatorStandalone: false })
    const blocked = () => {
      throw new DOMException('storage blocked', 'SecurityError')
    }
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(blocked)
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(blocked)

    render(<InstallHint />)
    expect(screen.getByRole('note')).toHaveTextContent(copy)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('note')).toBeNull()
  })
})
