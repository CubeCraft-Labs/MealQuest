// MealQuest service worker. vite-plugin-pwa replaces self.__WB_MANIFEST with
// the build's precache list, and installing caches every entry. Serving from
// that cache, expiring old entries and the update flow belong to the runtime
// caching strategies, which are not built yet.

export {}

declare const self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: Array<{ url: string; revision: string | null }>
}

const precacheName = 'mealquest-precache'
const precacheURLs = self.__WB_MANIFEST.map((entry) => entry.url)

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(precacheName).then((cache) => cache.addAll(precacheURLs)))
})
