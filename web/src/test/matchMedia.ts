import { act } from '@testing-library/react'

type Listener = (event: MediaQueryListEvent) => void

// Replaces window.matchMedia with one that answers min-width / max-width
// queries for a pretend viewport width (jsdom has no matchMedia and no
// layout). Returns a function that resizes the pretend viewport and notifies
// every query whose answer changed.
export function mockViewport(initialWidth: number): (width: number) => void {
  let width = initialWidth
  const lists: { list: MediaQueryList; listeners: Set<Listener> }[] = []

  function evaluate(query: string): boolean {
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

  return (next: number) => {
    const before = lists.map(({ list }) => list.matches)
    width = next
    act(() => {
      lists.forEach(({ list, listeners }, i) => {
        if (list.matches === before[i]) return
        const event = { matches: list.matches, media: list.media } as MediaQueryListEvent
        listeners.forEach((listener) => listener(event))
      })
    })
  }
}
