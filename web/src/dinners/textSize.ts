import { useState } from 'react'

// TEXT_SIZE_KEY is the localStorage key remembering, on this device, the text
// size chosen in Kitchen reading mode.
export const TEXT_SIZE_KEY = 'mealquest.readingTextSize'

export const TEXT_SIZES = ['small', 'medium', 'large'] as const

export type TextSize = (typeof TEXT_SIZES)[number]

// DEFAULT_TEXT_SIZE is the size of a device that has never chosen one: the
// handoff's 18 px ingredients and steps.
export const DEFAULT_TEXT_SIZE: TextSize = 'medium'

// TextSizeState is the text size, and the storage failure, if any, that
// stopped us reading or saving it.
type TextSizeState = {
  size: TextSize
  failure: { action: 'read' | 'write'; message: string } | null
}

function isTextSize(value: string): value is TextSize {
  return (TEXT_SIZES as readonly string[]).includes(value)
}

// Storage can be unavailable (blocked by the browser or privacy settings), in
// which case using it throws, and can hold a value MealQuest did not save.
// Either is reported, never treated as a remembered size.
function readTextSize(): TextSizeState {
  try {
    const saved = window.localStorage.getItem(TEXT_SIZE_KEY)
    if (saved === null) return { size: DEFAULT_TEXT_SIZE, failure: null }
    if (!isTextSize(saved)) throw new Error(`"${saved}" is not a text size MealQuest saved`)
    return { size: saved, failure: null }
  } catch (err) {
    return { size: DEFAULT_TEXT_SIZE, failure: { action: 'read', message: String(err) } }
  }
}

function writeTextSize(size: TextSize): TextSizeState {
  try {
    window.localStorage.setItem(TEXT_SIZE_KEY, size)
    return { size, failure: null }
  } catch (err) {
    return { size, failure: { action: 'write', message: String(err) } }
  }
}

export const textSizeFailureText = {
  read: 'Could not read the text size chosen on this device',
  write: 'Could not remember the text size on this device',
}

// useTextSize is Kitchen reading mode's text size on this device: the size,
// the storage failure if any, choose, which saves a size, and retry, which
// reads or saves again whatever failed. A size that could not be saved is
// still used until the screen is left.
export function useTextSize() {
  const [state, setState] = useState<TextSizeState>(readTextSize)
  const choose = (size: TextSize) => setState(writeTextSize(size))
  const retry = () => setState(state.failure?.action === 'write' ? writeTextSize(state.size) : readTextSize())
  return { ...state, choose, retry }
}
