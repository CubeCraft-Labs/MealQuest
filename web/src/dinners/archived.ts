import { useSyncExternalStore } from 'react'
import { ApiError } from '../api/errors.ts'

// ARCHIVED_PREFIX starts the localStorage key of each dinner this device
// removed — from Regulars or the Quest log — which the server archives. Each
// dinner has its own key (archivedKey), so two tabs removing two dinners at
// once each write their own and neither can drop the other's.
//
// A key holds REMOVING from before the removal is sent until the server
// answers, and ARCHIVED once it has archived the dinner. Archiving is for
// good — no dinner leaves archived — so an ARCHIVED key is never taken away.
// A REMOVING key stays when no answer said whether the server archived it,
// until the removal is sent again and answered (checkRemovals). Only a tab
// holding the dinner's Web Lock (removalLock) sends its removal or settles
// its key, so no tab settles it while another tab's removal is unanswered.
//
// Every read of a plan, Home, Supply run, the Regulars, the Quest log or a
// dinner's detail applies the record before anything shows (the select of
// usePlan, useHome, useSupplyRun, useRegulars, useQuestLog and
// useDinnerDetail): a dinner with either mark shows as one with no recipe to
// open, whatever any read says — one still in flight when it was archived,
// or one the service worker or this device saved before, read now or after
// a reload. The record is written before the removal is sent, so the server
// never archives a dinner this device has not recorded.
export const ARCHIVED_PREFIX = 'mealquest.archived-dinner.'
export const REMOVING = 'removing'
export const ARCHIVED = 'archived'

export function archivedKey(id: string): string {
  return ARCHIVED_PREFIX + id
}

// Archived is the record as saved on this device: the ids of the dinners
// with either mark, and why the record could not be read, or null.
export type Archived = { ids: ReadonlySet<string>; error: Error | null }

// ArchivedDinnerError is what the detail of a dinner the server archived
// reads as, whatever a read saved before says: it has no detail to show.
export class ArchivedDinnerError extends Error {
  constructor() {
    super('This dinner was removed.')
    this.name = 'ArchivedDinnerError'
  }
}

// UnconfirmedRemovalError is a removal no answer settled: the server may
// have archived the dinner, so it stays marked removing until the removal is
// sent again and answered (checkRemovals).
export class UnconfirmedRemovalError extends Error {
  constructor(cause: Error) {
    super(`${cause.message}. The server may have removed it, so this device hides it until the server can say whether it did.`, {
      cause,
    })
    this.name = 'UnconfirmedRemovalError'
  }
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

// marks is each dinner's mark saved on this device. A key under
// ARCHIVED_PREFIX holding anything else is not one MealQuest saved: an error.
function marks(): Map<string, string> {
  const found = new Map<string, string>()
  for (let i = 0; i < window.localStorage.length; i++) {
    const key = window.localStorage.key(i)
    if (key === null || !key.startsWith(ARCHIVED_PREFIX)) continue
    const mark = window.localStorage.getItem(key)
    if (mark !== REMOVING && mark !== ARCHIVED) {
      throw new Error(`The removed dinner saved on this device (${key}) is not one MealQuest saved.`)
    }
    found.set(key.slice(ARCHIVED_PREFIX.length), mark)
  }
  return found
}

// read is the record saved on this device.
function read(): Archived {
  try {
    return { ids: new Set(marks().keys()), error: null }
  } catch (error) {
    return { ids: new Set(), error: asError(error) }
  }
}

let current: Archived | undefined
const listeners = new Set<() => void>()
const archivedListeners = new Set<(id: string) => void>()

function snapshot(): Archived {
  current ??= read()
  return current
}

// changed reads the record again: what this page knows is always what the
// device saved.
function changed() {
  current = read()
  listeners.forEach((listener) => listener())
}

// archived marks dinner id archived and tells those listening (onArchived).
// The device refusing to save it leaves it marked removing, which hides it
// as well; sending the removal again (checkRemovals) tries again.
function archived(id: string) {
  try {
    window.localStorage.setItem(archivedKey(id), ARCHIVED)
  } catch {
    return
  }
  changed()
  archivedListeners.forEach((listener) => listener(id))
}

// Another tab of the app that marks a dinner changes the record here.
function onStorage(event: StorageEvent) {
  if (event.key !== null && !event.key.startsWith(ARCHIVED_PREFIX)) return
  changed()
  if (event.key !== null && event.newValue === ARCHIVED) {
    const id = event.key.slice(ARCHIVED_PREFIX.length)
    archivedListeners.forEach((listener) => listener(id))
  }
}

function subscribe(listener: () => void) {
  if (listeners.size === 0 && archivedListeners.size === 0) window.addEventListener('storage', onStorage)
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && archivedListeners.size === 0) window.removeEventListener('storage', onStorage)
  }
}

// onArchived calls listener with each dinner marked archived, here or in
// another tab, until the returned function is called.
export function onArchived(listener: (id: string) => void): () => void {
  if (listeners.size === 0 && archivedListeners.size === 0) window.addEventListener('storage', onStorage)
  archivedListeners.add(listener)
  return () => {
    archivedListeners.delete(listener)
    if (listeners.size === 0 && archivedListeners.size === 0) window.removeEventListener('storage', onStorage)
  }
}

// removalLock names the Web Lock held, across every tab of the app, while a
// removal of dinner id is sent and until its answer is taken in. Only the
// holder settles the dinner's mark, so no tab settles it while another
// tab's removal is unanswered; a tab that closes lets the lock go, and the
// next holder sends the removal again.
export function removalLock(id: string): string {
  return 'mealquest.removal.' + id
}

// locks is the browser's Web Locks, without which tabs cannot keep from
// settling one removal at once: an error.
function locks(): LockManager {
  if (typeof navigator.locks === 'undefined') {
    throw new Error('this browser cannot keep its tabs from settling one removal at once (no Web Locks), so it was not sent')
  }
  return navigator.locks
}

// markRemoving marks dinner id removing; a device that refuses to save that
// is an error, and nothing may be sent.
function markRemoving(id: string) {
  try {
    window.localStorage.setItem(archivedKey(id), REMOVING)
  } catch (error) {
    throw new Error(`this device could not save the removal, so it was not sent: ${asError(error).message}`, { cause: error })
  }
  changed()
}

// The dinners this page is settling (checkRemovals), so no second check
// here waits to settle one again.
const checking = new Set<string>()

// SendRemoval sends dinner id's removal, which the server archives, and is
// settled only by the server's own answer to it.
export type SendRemoval = (id: string) => Promise<unknown>

// settle sends dinner id's removal while this page holds its lock and marks
// it as the server answers. Archived — or not found, so it has no detail
// either — it is marked archived. Refused, its mark is taken away. With no
// answer, it stays marked removing and the removal fails with
// UnconfirmedRemovalError.
async function settle(id: string, send: SendRemoval): Promise<void> {
  try {
    await send(id)
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      archived(id)
    } else if (error instanceof ApiError) {
      window.localStorage.removeItem(archivedKey(id))
      changed()
    } else {
      throw new UnconfirmedRemovalError(asError(error))
    }
    throw error
  }
  archived(id)
}

// archiveDinner removes dinner id with send (its DELETE). It is marked
// removing before anything is sent; a device that refuses to save that is
// an error and nothing is sent. The removal is sent holding the dinner's
// lock (removalLock) and settled as the server answers (settle) — unless
// another tab, holding it first, already had it archived. With no answer,
// the server is sent it again at once (checkRemovals).
export async function archiveDinner(id: string, send: SendRemoval): Promise<void> {
  const manager = locks()
  markRemoving(id)
  try {
    await manager.request(removalLock(id), async () => {
      const mark = window.localStorage.getItem(archivedKey(id))
      if (mark === ARCHIVED) return
      // Another tab's removal was refused meanwhile and took the mark away:
      // this is the household asking again.
      if (mark !== REMOVING) markRemoving(id)
      await settle(id, send)
    })
  } catch (error) {
    if (error instanceof UnconfirmedRemovalError) void checkRemovals(send)
    throw error
  }
}

// checkRemovals settles each dinner marked removing that this page is not
// settling already: it waits for the dinner's lock — held while a removal
// of it is unanswered, here or in another tab — and, if it is still marked
// removing then, sends its removal again (settle). The household's removal
// is never undone on the strength of a read that may have come before
// another tab's removal: only the server's answer to a removal settles it.
// One the server does not answer stays marked removing — hidden, as it may
// be archived — until the next check: when the app starts and each time the
// server can be reached again (useRemovalCheck).
export async function checkRemovals(send: SendRemoval): Promise<void> {
  let saved: Map<string, string>
  try {
    saved = marks()
  } catch {
    // The record cannot be read; every read reports that already.
    return
  }
  const manager = locks()
  const unsettled = [...saved].filter(([id, mark]) => mark === REMOVING && !checking.has(id))
  await Promise.all(
    unsettled.map(async ([id]) => {
      checking.add(id)
      try {
        await manager.request(removalLock(id), async () => {
          // A removal answered meanwhile, here or in another tab, settled it.
          if (window.localStorage.getItem(archivedKey(id)) !== REMOVING) return
          await settle(id, send)
        })
      } catch {
        // Settled as the server answered, or still marked removing until
        // the next check.
      } finally {
        checking.delete(id)
      }
    }),
  )
}

// knownArchived throws why the record saved on this device could not be
// read, if it could not, and otherwise is the ids of the dinners marked: no
// read may show what the record would hide while the record is unknown.
export function knownArchived(record: Archived): ReadonlySet<string> {
  if (record.error !== null) throw record.error
  return record.ids
}

// archivedNow is the record as this page knows it now, for what saves a
// read outside a render (useSupplyRunSync).
export function archivedNow(): Archived {
  return snapshot()
}

// useArchived is the record, rendering again as it changes.
export function useArchived(): Archived {
  return useSyncExternalStore(subscribe, snapshot)
}

// resetArchived forgets what this page knows, as a new page starts: each
// test starts with it.
export function resetArchived() {
  current = undefined
  checking.clear()
}
