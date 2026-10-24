import { useSyncExternalStore } from 'react'

// ARCHIVED_KEY is the localStorage key holding, on this device, the ids of
// the dinners the server archived when they were removed from Regulars here.
// Archiving is for good — no dinner leaves archived — so the record only
// grows, and what it holds is true whatever any read says.
//
// Every read of a plan, Home, Supply run, the Regulars or a dinner's detail
// applies it before anything shows (the select of usePlan, useHome,
// useSupplyRun, useRegulars and useDinnerDetail), so a read the server
// answered before the archive — one still in flight when it was archived, or
// one the service worker or this device saved before, read now or offline
// later — can never show such a dinner as one with a recipe to open.
export const ARCHIVED_KEY = 'mealquest.archived-dinners'

// Archived is the record as this page knows it: the ids, and why the record
// saved on this device could not be read, or null.
export type Archived = { ids: ReadonlySet<string>; error: Error | null }

// ArchivedDinnerError is what the detail of a dinner the server archived
// reads as, whatever a read saved before says: it has no detail to show.
export class ArchivedDinnerError extends Error {
  constructor() {
    super('This dinner was removed from Regulars.')
    this.name = 'ArchivedDinnerError'
  }
}

// read is the record saved on this device. One that is not a list of ids
// MealQuest saved is an error.
function read(): Archived {
  try {
    const text = window.localStorage.getItem(ARCHIVED_KEY)
    const saved: unknown = text === null ? [] : JSON.parse(text)
    if (!Array.isArray(saved) || !saved.every((id) => typeof id === 'string')) {
      throw new Error(`The archived dinners saved on this device (${ARCHIVED_KEY}) are not a list MealQuest saved.`)
    }
    return { ids: new Set(saved), error: null }
  } catch (error) {
    return { ids: new Set(), error: error instanceof Error ? error : new Error(String(error)) }
  }
}

let current: Archived | undefined
const listeners = new Set<() => void>()

function snapshot(): Archived {
  current ??= read()
  return current
}

function changed(next: Archived) {
  current = next
  listeners.forEach((listener) => listener())
}

// Another tab of the app that records an archive changes the record here.
function onStorage(event: StorageEvent) {
  if (event.key === ARCHIVED_KEY) changed(read())
}

function subscribe(listener: () => void) {
  if (listeners.size === 0) window.addEventListener('storage', onStorage)
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) window.removeEventListener('storage', onStorage)
  }
}

// recordArchived records that the server archived dinner id. From then on
// this page shows it archived, whether or not the record could be saved on
// the device; the answer is why it could not be — the record saved here
// could not be read, or the device refused to keep it — so that after a
// reload a read saved before may still show the dinner as it was — or null
// once it is saved. A record that could not be read is left as it is.
export function recordArchived(id: string): Error | null {
  const before = snapshot()
  const ids = new Set(before.ids).add(id)
  if (before.error !== null) {
    changed({ ids, error: before.error })
    return before.error
  }
  try {
    window.localStorage.setItem(ARCHIVED_KEY, JSON.stringify([...ids]))
  } catch (error) {
    changed({ ids, error: null })
    return error instanceof Error ? error : new Error(String(error))
  }
  changed({ ids, error: null })
  return null
}

// knownArchived throws why the record saved on this device could not be
// read, if it could not, and otherwise is the ids the server archived: no
// read may show what the record would hide while the record is unknown.
export function knownArchived(archived: Archived): ReadonlySet<string> {
  if (archived.error !== null) throw archived.error
  return archived.ids
}

// useArchived is the record, rendering again as it changes.
export function useArchived(): Archived {
  return useSyncExternalStore(subscribe, snapshot)
}

// resetArchived forgets the record this page knows, as a new page starts:
// each test starts with it.
export function resetArchived() {
  current = undefined
}
