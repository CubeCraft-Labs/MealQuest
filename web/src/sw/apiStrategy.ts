// The network-first strategy for API reads, kept apart from src/sw.ts so the
// tests can run it without a service worker.
import type { WorkboxPlugin } from 'workbox-core/types'
import { NetworkFirst } from 'workbox-strategies'
import { cacheNames, markCacheFallback, networkTimeoutSeconds } from './routes'

// NetworkFirst reads its cache only when the network fails or times out, so
// every cached response it uses is a fallback.
const markFallback: WorkboxPlugin = {
  cachedResponseWillBeUsed: async ({ cachedResponse }) =>
    cachedResponse === undefined ? undefined : markCacheFallback(cachedResponse),
}

export function createApiStrategy(): NetworkFirst {
  return new NetworkFirst({ cacheName: cacheNames.api, networkTimeoutSeconds, plugins: [markFallback] })
}
