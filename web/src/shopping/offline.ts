import { useSyncExternalStore } from 'react'
import { isRecord } from '../api/errors.ts'

// SAVED_KEY is the localStorage key holding, on this device, what the Supply
// run screen needs to open offline — the household's schedule and the
// Supply runs last read — and the toggles made that the server has not
// answered yet, in the order they were made.
export const SAVED_KEY = 'mealquest.supply-run'

// SAVED_WEEKS is how many weeks' Supply runs are kept: this week's and next
// week's are the only ones still current.
const SAVED_WEEKS = 2

// Schedule is the part of the household settings the Supply run screen
// needs to choose the week and say times. The service worker never caches
// the settings, so it is kept here for the screen to open offline.
export type Schedule = { timezone: string; plan_draft_dow: number; plan_draft_time: string }

// QueuedToggle is a toggle the server has not answered yet: key tells it
// apart from any other toggle of the same item.
export type QueuedToggle = { key: string; weekStart: string; id: string; needed: boolean }

// Saved is the stored value: runs holds each week's Supply run as the server
// last sent it, by its Monday.
type Saved = { schedule: Schedule | null; runs: Record<string, unknown>; queue: QueuedToggle[] }

function isSchedule(value: unknown): value is Schedule {
  return (
    isRecord(value) &&
    typeof value.timezone === 'string' &&
    Number.isInteger(value.plan_draft_dow) &&
    typeof value.plan_draft_time === 'string'
  )
}

function isQueuedToggle(value: unknown): value is QueuedToggle {
  return (
    isRecord(value) &&
    typeof value.key === 'string' &&
    typeof value.weekStart === 'string' &&
    typeof value.id === 'string' &&
    typeof value.needed === 'boolean'
  )
}

// read is the stored value, empty when nothing is stored. A value that is not
// one MealQuest stored is an error.
function read(): Saved {
  const text = window.localStorage.getItem(SAVED_KEY)
  if (text === null) return { schedule: null, runs: {}, queue: [] }
  const saved: unknown = JSON.parse(text)
  if (
    !isRecord(saved) ||
    (saved.schedule !== null && !isSchedule(saved.schedule)) ||
    !isRecord(saved.runs) ||
    !Array.isArray(saved.queue) ||
    !saved.queue.every(isQueuedToggle)
  ) {
    throw new Error(`The Supply run saved on this device (${SAVED_KEY}) is not one MealQuest saved.`)
  }
  return { schedule: saved.schedule as Schedule | null, runs: saved.runs, queue: saved.queue }
}

function write(change: (saved: Saved) => Saved) {
  window.localStorage.setItem(SAVED_KEY, JSON.stringify(change(read())))
}

// savedSchedule is the household's schedule as last read, or null.
export function savedSchedule(): Schedule | null {
  return read().schedule
}

export function saveSchedule({ timezone, plan_draft_dow, plan_draft_time }: Schedule) {
  write((saved) => ({ ...saved, schedule: { timezone, plan_draft_dow, plan_draft_time } }))
}

// savedRun is the week's Supply run as the server last sent it, unparsed, or
// undefined when none is saved.
export function savedRun(weekStart: string): unknown {
  return read().runs[weekStart]
}

// saveRun keeps the week's Supply run as the server sent it, with the latest
// weeks'. A week with none (null) or one frozen into the Supply archive is
// not kept: nothing on it can change.
export function saveRun(weekStart: string, run: { status: string } | null) {
  write((saved) => {
    const runs = { ...saved.runs }
    delete runs[weekStart]
    if (run !== null && run.status !== 'archived') runs[weekStart] = run
    const latest = Object.keys(runs).sort().slice(-SAVED_WEEKS)
    return { ...saved, runs: Object.fromEntries(latest.map((week) => [week, runs[week]])) }
  })
}

// queued is the toggles the server has not answered, in the order made.
export function queued(): QueuedToggle[] {
  return read().queue
}

export function enqueue(toggle: QueuedToggle) {
  write((saved) => ({ ...saved, queue: [...saved.queue, toggle] }))
}

export function dequeue(key: string) {
  write((saved) => ({ ...saved, queue: saved.queue.filter((toggle) => toggle.key !== key) }))
}

// The latest failure to read or write the saved Supply run, which the Supply
// run screen shows: a toggle that cannot be kept may be lost on a reload.
let problem: Error | null = null
const listeners = new Set<() => void>()

// keep runs one read or write of the saved Supply run, and returns its
// result, or undefined when it failed: the failure is reported to the Supply
// run screen. Listeners hear of it after the current render.
export function keep<T>(action: () => T): T | undefined {
  try {
    return action()
  } catch (error) {
    problem = error instanceof Error ? error : new Error(String(error))
    queueMicrotask(() => listeners.forEach((listener) => listener()))
    return undefined
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

// useSavedProblem is the latest failure to read or write the saved Supply
// run, or null.
export function useSavedProblem(): Error | null {
  return useSyncExternalStore(subscribe, () => problem)
}
