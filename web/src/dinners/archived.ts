import { useSyncExternalStore } from 'react'
import { ApiError } from '../api/errors.ts'

// ARCHIVED_PREFIX starts the localStorage key of each dinner this device
// removed — from Regulars or the Quest log — which the server archives. Each
// dinner has its own key (archivedKey), so two tabs removing two dinners at
// once each write their own and neither can drop the other's.
//
// Each removal has an id this device chose (a UUID), sent with it: the
// server records the removal as made when it archives the dinner, and a
// removal it has recorded withdrawn it never makes, however late it
// arrives. A key holds REMOVING and the removal's id (removingMark) from
// before the removal is first sent until the server has settled it, and
// ARCHIVED once the server has archived the dinner. Archiving is for good —
// no dinner leaves archived — so an ARCHIVED key is never taken away or
// marked removing again. Only an answer that the server made the removal
// marks it ARCHIVED, and only the server's answer that it withdrew the
// removal takes the key away: no refusal, and no answer, says a send of it
// that went unanswered — from a tab that closed, say — will not still
// arrive and be made. Until the server settles it, a REMOVING key stays and
// the removal is sent again (checkRemovals). Only a tab holding the
// dinner's Web Lock (removalLock) reads and writes its key, or sends or
// withdraws its removal, so no tab settles it while another is settling it.
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

// removingMark is the mark of a dinner whose removal (its id) the server
// has not settled.
export function removingMark(removal: string): string {
  return `${REMOVING}:${removal}`
}

// A removal id is a UUID in canonical lower-case form (crypto.randomUUID),
// as the server requires.
const REMOVAL_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

// Mark is a dinner's mark: archived, or removing with its removal's id.
type Mark = { archived: true } | { archived: false; removal: string }

// parseMark is the mark saved under key; one MealQuest did not save is an
// error.
function parseMark(key: string, saved: string): Mark {
  if (saved === ARCHIVED) return { archived: true }
  const removal = saved.startsWith(REMOVING + ':') ? saved.slice(REMOVING.length + 1) : ''
  if (!REMOVAL_ID.test(removal)) {
    throw new Error(`The removed dinner saved on this device (${key}) is not one MealQuest saved.`)
  }
  return { archived: false, removal }
}

// markOf is dinner id's mark, or null when it has none.
function markOf(id: string): Mark | null {
  const saved = window.localStorage.getItem(archivedKey(id))
  return saved === null ? null : parseMark(archivedKey(id), saved)
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

// UnconfirmedRemovalError is a removal the server did not settle: it may
// have archived the dinner, so it stays marked removing until the removal is
// sent again and settled (checkRemovals).
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
function marks(): Map<string, Mark> {
  const found = new Map<string, Mark>()
  for (let i = 0; i < window.localStorage.length; i++) {
    const key = window.localStorage.key(i)
    if (key === null || !key.startsWith(ARCHIVED_PREFIX)) continue
    found.set(key.slice(ARCHIVED_PREFIX.length), parseMark(key, window.localStorage.getItem(key) ?? ''))
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
// as well; sending the removal again (checkRemovals), which the server has
// made, tries again.
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

// removalLock names the Web Lock held, across every tab of the app, while
// dinner id's mark is read and written and its removal sent or withdrawn.
// Only the holder settles the dinner's mark, so no tab settles it while
// another tab is settling it; a tab that closes lets the lock go, and the
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

// markRemoving marks dinner id removing with a new removal's id and is that
// id; a device that cannot make the id or refuses to save the mark is an
// error, and nothing may be sent.
function markRemoving(id: string): string {
  let removal: string
  try {
    removal = crypto.randomUUID()
    window.localStorage.setItem(archivedKey(id), removingMark(removal))
  } catch (error) {
    throw new Error(`this device could not save the removal, so it was not sent: ${asError(error).message}`, { cause: error })
  }
  changed()
  return removal
}

// The dinners this page is settling (checkRemovals), so no second check
// here waits to settle one again.
const checking = new Set<string>()

// RemovalOutcome is how the server settled a removal it was asked to
// withdraw: made (archived) or never to be made (withdrawn).
export type RemovalOutcome = 'archived' | 'withdrawn'

// Removals is how removals reach the server: send sends dinner id's removal
// with its id (DELETE), which the server archives; withdraw asks the server
// to settle that removal for good and is how it did.
export type Removals = {
  send: (id: string, removal: string) => Promise<unknown>
  withdraw: (id: string, removal: string) => Promise<RemovalOutcome>
}

function notFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404
}

// settle sends dinner id's removal while this page holds its lock and marks
// it as the server settles it. Made — or the dinner not found, so it has no
// detail either — it is marked archived. Answered any other way, or not at
// all, the server is asked to withdraw it, since neither says that a send
// of it before, unanswered, will not still arrive and be made: made after
// all, it is marked archived and the removal succeeds; withdrawn, its mark
// is taken away and the removal fails as it was answered. With no answer to
// that either, it stays marked removing and the removal fails with
// UnconfirmedRemovalError.
async function settle(id: string, removal: string, removals: Removals): Promise<void> {
  let answer: unknown
  try {
    await removals.send(id, removal)
    archived(id)
    return
  } catch (error) {
    if (notFound(error)) {
      archived(id)
      throw error
    }
    answer = error
  }
  let outcome: RemovalOutcome
  try {
    outcome = await removals.withdraw(id, removal)
  } catch (error) {
    if (notFound(error)) {
      archived(id)
      throw answer
    }
    throw new UnconfirmedRemovalError(asError(answer))
  }
  if (outcome === 'archived') {
    archived(id)
    return
  }
  window.localStorage.removeItem(archivedKey(id))
  changed()
  throw answer
}

// archiveDinner removes dinner id with removals. Holding the dinner's lock
// (removalLock), it reads the dinner's mark: one already archived — by
// another tab, perhaps, while this one showed it still — stays archived and
// nothing is sent. Otherwise the removal is sent and settled as the server
// answers (settle): the one already marked removing, sent again, or a new
// one, marked removing before it is sent; a device that refuses to save
// that is an error and nothing is sent. With no answer, the server is sent
// it again at once (checkRemovals).
export async function archiveDinner(id: string, removals: Removals): Promise<void> {
  const manager = locks()
  try {
    await manager.request(removalLock(id), async () => {
      const mark = markOf(id)
      if (mark?.archived === true) return
      await settle(id, mark === null ? markRemoving(id) : mark.removal, removals)
    })
  } catch (error) {
    if (error instanceof UnconfirmedRemovalError) void checkRemovals(removals)
    throw error
  }
}

// checkRemovals settles each dinner marked removing that this page is not
// settling already: it waits for the dinner's lock — held while a tab
// settles it — and, if it is still marked removing then, sends its removal
// again (settle). Only the server settles a removal: its answer that it
// made it, or that it withdrew it. One the server does not settle stays
// marked removing — hidden, as it may be made — until the next check: when
// the app starts and each time the server can be reached again
// (useRemovalCheck).
export async function checkRemovals(removals: Removals): Promise<void> {
  let saved: Map<string, Mark>
  try {
    saved = marks()
  } catch {
    // The record cannot be read; every read reports that already.
    return
  }
  const manager = locks()
  const unsettled = [...saved.entries()].filter(([id, mark]) => !mark.archived && !checking.has(id))
  await Promise.all(
    unsettled.map(async ([id]) => {
      checking.add(id)
      try {
        await manager.request(removalLock(id), async () => {
          // A tab settled it meanwhile, or the household removed it again.
          const mark = markOf(id)
          if (mark === null || mark.archived) return
          await settle(id, mark.removal, removals)
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
