// What the page changes in the service worker's API cache. The worker saves
// every network-first read (routes.ts) and answers with it when the server
// cannot be reached, so a read saved before a change still says what was
// true then.
import { cacheNames } from './routes'

// forgetCachedDinner deletes every read the worker saved that holds dinner
// id — its plans, Supply runs, Home, Regulars and detail — so none can later
// answer for it as it was before it changed. Reads without it are kept, so
// they still open offline. Without Cache Storage (an origin that is not
// secure) there is no service worker, so nothing was saved.
export async function forgetCachedDinner(id: string): Promise<void> {
  if (typeof caches === 'undefined') return
  const cache = await caches.open(cacheNames.api)
  const quoted = JSON.stringify(id)
  await Promise.all(
    (await cache.keys()).map(async (request) => {
      const response = await cache.match(request)
      if (response !== undefined && (await response.text()).includes(quoted)) await cache.delete(request)
    }),
  )
}
