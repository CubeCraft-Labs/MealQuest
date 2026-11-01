import { act } from '@testing-library/react'

type Listener = (event: MediaQueryListEvent) => void

// Replaces window.matchMedia with one that answers min-width / max-width
// queries for a pretend viewport width, orientation: portrait for its height
// (a viewport given no height is landscape), and prefers-reduced-motion:
// reduce with reducedMotion (jsdom has no matchMedia and no layout). Returns
// a function that resizes the pretend viewport and notifies every query
// whose answer changed.
export function mockViewport(
  initialWidth: number,
  { reducedMotion = false, height: initialHeight = 0 } = {},
): (width: number, height?: number) => void {
  let width = initialWidth
  let height = initialHeight
  const lists: { list: MediaQueryList; listeners: Set<Listener> }[] = []

  function evaluate(query: string): boolean {
    if (query === '(prefers-reduced-motion: reduce)') return reducedMotion
    if (query === '(orientation: portrait)') return height >= width
    const min = /\(min-width:\s*(\d+)px\)/.exec(query)
    const max = /\(max-width:\s*(\d+)px\)/.exec(query)
    if (!min && !max) throw new Error(`mockViewport cannot evaluate ${JSON.stringify(query)}`)
    return (!min || width >= Number(min[1])) && (!max || width <= Number(max[1]))
  }

  window.matchMedia = (query: string) => {
    const listeners = new Set<Listener>()
    const list = {
      media: query,
      get matches() {
        return evaluate(query)
      },
      onchange: null,
      addEventListener: (_type: string, listener: Listener) => listeners.add(listener),
      removeEventListener: (_type: string, listener: Listener) => listeners.delete(listener),
      addListener: (listener: Listener) => listeners.add(listener),
      removeListener: (listener: Listener) => listeners.delete(listener),
      dispatchEvent: () => true,
    } as unknown as MediaQueryList
    lists.push({ list, listeners })
    return list
  }

  return (nextWidth: number, nextHeight = 0) => {
    const before = lists.map(({ list }) => list.matches)
    width = nextWidth
    height = nextHeight
    act(() => {
      lists.forEach(({ list, listeners }, i) => {
        if (list.matches === before[i]) return
        const event = { matches: list.matches, media: list.media } as MediaQueryListEvent
        listeners.forEach((listener) => listener(event))
      })
    })
  }
}
