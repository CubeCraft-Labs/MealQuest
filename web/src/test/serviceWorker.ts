// Runs requests through the service worker's real API strategy against an
// in-memory cache storage: jsdom has neither a service worker nor caches.
import { expect, vi } from 'vitest'
import type { RouteHandlerCallbackOptions } from 'workbox-core/types'
import { createApiStrategy } from '../sw/apiStrategy.ts'
import { cacheNames, routeFor } from '../sw/routes.ts'

export const origin = 'https://mealquest.test'

// FakeCache keys entries by URL, which is all the API cache needs.
export class FakeCache {
  readonly entries = new Map<string, Response>()
  async match(request: RequestInfo | URL): Promise<Response | undefined> {
    return this.entries.get(new Request(request).url)?.clone()
  }
  async put(request: RequestInfo | URL, response: Response): Promise<void> {
    this.entries.set(new Request(request).url, response)
  }
}

// FakeExtendableEvent stands in for the worker-only ExtendableEvent,
// collecting the promises the strategy waits on.
class FakeExtendableEvent extends Event {
  readonly pending: Promise<unknown>[] = []
  waitUntil(promise: Promise<unknown>) {
    this.pending.push(promise)
  }
}

// stubWorkerCaches stubs the worker globals Workbox checks and a cache
// storage holding only the API cache, which it returns. Undo with
// vi.unstubAllGlobals.
export function stubWorkerCaches(): FakeCache {
  const cache = new FakeCache()
  vi.stubGlobal('FetchEvent', class FetchEvent {})
  vi.stubGlobal('ExtendableEvent', FakeExtendableEvent)
  const only = (name: string | undefined) => {
    if (name !== cacheNames.api) throw new Error(`unexpected cache ${name}`)
    return cache
  }
  vi.stubGlobal('caches', {
    open: async (name: string) => only(name),
    match: async (request: RequestInfo | URL, options?: MultiCacheQueryOptions) =>
      only(options?.cacheName).match(request),
  })
  return cache
}

// answer sends a GET through the API strategy, as src/sw.ts does for a
// network-first route; the strategy's own fetch is the network.
export async function answer(request: Request): Promise<Response> {
  expect(routeFor({ method: request.method, url: new URL(request.url), mode: 'cors' }, origin)).toEqual({
    strategy: 'network-first',
    cacheName: cacheNames.api,
  })
  const event = new FakeExtendableEvent('fetch')
  const response = await createApiStrategy().handle({ request, event: event as unknown as RouteHandlerCallbackOptions['event'] })
  await Promise.all(event.pending)
  return response
}

// stubFetchThroughWorker replaces the global fetch with the page's view of
// it behind the worker, routed as src/sw.ts routes it: a network-first path
// from the API client goes through the API strategy, a network-only one
// straight to network; the strategy's own fetch, a Request, is network too.
// As in a browser, a worker that cannot answer is a network error to the
// page. Undo with vi.unstubAllGlobals.
export function stubFetchThroughWorker(network: (request: Request) => Promise<Response>) {
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    if (input instanceof Request) return network(input)
    const request = new Request(new URL(String(input), origin), init)
    const route = routeFor({ method: request.method, url: new URL(request.url), mode: 'cors' }, origin)
    if (route.strategy === 'network-only') return network(request)
    if (route.strategy !== 'network-first') throw new Error(`unexpected ${route.strategy} fetch: ${request.url}`)
    return answer(request).catch((error: unknown) => {
      throw new TypeError('Failed to fetch', { cause: error })
    })
  })
}
