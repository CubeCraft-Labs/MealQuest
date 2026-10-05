// NetworkWindow is the part of window the network status reads: the browser's
// online flag and its online/offline events.
export type NetworkWindow = Pick<Window, 'navigator' | 'addEventListener' | 'removeEventListener'>

export type NetworkStatus = {
  // offline reports whether the browser says it is offline or the last API
  // request failed with a network error or was answered from the cache.
  offline(): boolean
  // subscribe is notified when offline may have changed. It listens for the
  // window's online/offline events until the returned function is called.
  subscribe(listener: () => void): () => void
  // requestFailed records a fetch that rejected with a network error or that
  // the service worker answered from its cache.
  requestFailed(): void
  // requestSucceeded records a fetch the server answered.
  requestSucceeded(): void
}

// createNetworkStatus tracks whether MealQuest can reach the server. A failed
// request counts as offline until a later request gets a response or the
// browser reports that it is back online.
export function createNetworkStatus(win: NetworkWindow): NetworkStatus {
  let failed = false
  const listeners = new Set<() => void>()

  function setFailed(value: boolean) {
    if (failed === value) return
    failed = value
    listeners.forEach((listener) => listener())
  }

  return {
    offline: () => !win.navigator.onLine || failed,
    subscribe(listener) {
      const online = () => {
        failed = false
        listener()
      }
      listeners.add(listener)
      win.addEventListener('online', online)
      win.addEventListener('offline', listener)
      return () => {
        listeners.delete(listener)
        win.removeEventListener('online', online)
        win.removeEventListener('offline', listener)
      }
    },
    requestFailed: () => setFailed(true),
    requestSucceeded: () => setFailed(false),
  }
}
