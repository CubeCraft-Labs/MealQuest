import { useSyncExternalStore } from 'react'
import { ApiError } from '../api/errors.ts'

// ARCHIVED_PREFIX starts the localStorage key of each dinner this device
// removed — from Regulars or the Quest log — which the server archives. Each
// dinner has its own key (archivedKey), so two tabs removing two dinners at
// once each write their own and neither can drop the other's.
//
// A key holds REMOVING from before the removal is first sent until the
// server archives the dinner, and ARCHIVED from then on. A key is written
// once and never taken away: once this device has begun to remove a
// dinner, it hides it from recipe links for good, whatever any read, or
// any other device, says. Nothing here undoes a removal on the strength of
// what the server said at one moment — another device may archive the
// dinner the next — so there is nothing a later archive can race. A
// REMOVING key turns ARCHIVED only on the server's own answer to the
// removal: archived, or the dinner not found. Archiving a dinner again
// succeeds (DELETE /api/v1/dinners/{id}), so a removal refused or not
// answered stays REMOVING and is sent again until it is answered
// (checkRemovals). Only a tab holding the dinner's Web Lock (removalLock)
// reads and writes its key or sends its removal, so no tab marks an
// ARCHIVED dinner REMOVING again.
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

// RefusedRemovalError is a removal the server refused: it stays marked
// removing, so hidden, and is sent again until the server archives it
// (checkRemovals).
export class RefusedRemovalError extends Error {
  constructor(cause: Error) {
    super(`${cause.message}. It could not be removed here; it stays hidden on this device until the server settles it.`, { cause })
    this.name = 'RefusedRemovalError'
  }
}

// UnsavedArchiveError is a removal the server archived that this device
// could not save as archived: it stays marked removing, so hidden, and is
// sent again until the device saves it (checkRemovals).
export class UnsavedArchiveError extends Error {
  constructor(cause: Error) {
    super(`this device could not save that the server removed it, so it stays hidden here and is asked about again: ${cause.message}`, {
      cause,
    })
    this.name = 'UnsavedArchiveError'
  }
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

// marks is each dinner's mark saved on this device. A key under
// ARCHIVED_PREFIX holding anything else is not one MealQuest saved: an error.
function marks(): Map<string, Mark> {
  const found = new Map<string, Mark>()
  for (let i = 0; i < window.localStorage.length; i++) {
    const key = window.localStorage.key(i)
    if (key === null || !key.startsWith(ARCHIVED_PREFIX)) continue
    found.set(key.slice(ARCHIVED_PREFIX.length), parseMark(key, window.localStorage.getItem(key)))
  }
  return found
}

// Mark is a dinner's mark.
type Mark = typeof REMOVING | typeof ARCHIVED

// parseMark is the mark saved under key; one MealQuest did not save is an
// error.
function parseMark(key: string, saved: string | null): Mark {
  if (saved !== REMOVING && saved !== ARCHIVED) {
    throw new Error(`The removed dinner saved on this device (${key}) is not one MealQuest saved.`)
  }
  return saved
}

// markOf is dinner id's mark, or null when it has none.
function markOf(id: string): Mark | null {
  const saved = window.localStorage.getItem(archivedKey(id))
  return saved === null ? null : parseMark(archivedKey(id), saved)
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

// archived marks dinner id, which the server has archived, archived and
// tells those listening (onArchived). The device refusing to save the mark
// leaves it marked removing, which hides it as well, and sending the removal
// again (checkRemovals) tries again; those listening are told all the same,
// since the server did archive it, and the refusal fails with
// UnsavedArchiveError.
function archived(id: string) {
  let refused: Error | null = null
  try {
    window.localStorage.setItem(archivedKey(id), ARCHIVED)
  } catch (error) {
    refused = asError(error)
  }
  if (refused === null) changed()
  archivedListeners.forEach((listener) => listener(id))
  if (refused !== null) throw new UnsavedArchiveError(refused)
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

// markRemoving marks dinner id, which has no mark, removing; a device that
// refuses to save that is an error, and nothing may be sent.
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

// settle sends dinner id's removal while this page holds its lock. Archived
// — or not found, so it has no detail either — it is marked archived
// (archived; a device that refuses to save that fails with
// UnsavedArchiveError). Refused, it stays marked removing and the removal
// fails with RefusedRemovalError; with no answer, it stays marked removing
// and fails with UnconfirmedRemovalError. No answer takes a mark away.
async function settle(id: string, send: SendRemoval): Promise<void> {
  try {
    await send(id)
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      archived(id)
      throw error
    }
    if (error instanceof ApiError) throw new RefusedRemovalError(error)
    throw new UnconfirmedRemovalError(asError(error))
  }
  archived(id)
}

// archiveDinner removes dinner id with send (its DELETE). Holding the
// dinner's lock (removalLock), it reads the dinner's mark: one already
// archived — by another tab, perhaps, while this one showed it still —
// stays archived and nothing is sent. One with no mark is marked removing
// before anything is sent; a device that refuses to save that is an error
// and nothing is sent. The removal is then sent and settled as the server
// answers (settle). With no answer, the server is sent it again at once
// (checkRemovals).
export async function archiveDinner(id: string, send: SendRemoval): Promise<void> {
  const manager = locks()
  try {
    await manager.request(removalLock(id), async () => {
      const mark = markOf(id)
      if (mark === ARCHIVED) return
      if (mark === null) markRemoving(id)
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
// removing then, sends its removal again (settle). Only the server's answer
// that it archived the dinner settles it; one the server refuses or does
// not answer stays marked removing — hidden — until the next check: when
// the app starts and each time the server can be reached again
// (useRemovalCheck).
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
          if (markOf(id) !== REMOVING) return
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

// markedNow says whether dinner id has either mark, read from this device at
// the moment of asking rather than from what this page last knew: another
// tab's mark counts before its storage event arrives here. A mark MealQuest
// did not save is an error.
export function markedNow(id: string): boolean {
  return markOf(id) !== null
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
