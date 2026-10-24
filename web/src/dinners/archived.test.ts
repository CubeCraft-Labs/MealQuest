import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors.ts'
import {
  ARCHIVED,
  archivedKey,
  archiveDinner,
  checkRemovals,
  knownArchived,
  onArchived,
  REMOVING,
  resetArchived,
  UnconfirmedRemovalError,
  useArchived,
} from './archived.ts'

afterEach(() => {
  vi.restoreAllMocks()
})

const sent = () => Promise.resolve(undefined)
const neverAsked = () => Promise.reject(new Error('not asked'))

// marks is every dinner's mark saved on this device.
function marks() {
  return Object.fromEntries(Object.keys(localStorage).sort().map((key) => [key, localStorage.getItem(key)]))
}

describe('archiveDinner', () => {
  it('marks the dinner removing before the removal is sent, then archived, so a new page knows it', async () => {
    let markWhenSent: string | null = null
    await archiveDinner(
      'd1',
      () => {
        markWhenSent = localStorage.getItem(archivedKey('d1'))
        return sent()
      },
      neverAsked,
    )
    await archiveDinner('d2', sent, neverAsked)
    expect(markWhenSent).toBe(REMOVING)
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED, [archivedKey('d2')]: ARCHIVED })

    resetArchived()
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)].sort()).toEqual(['d1', 'd2'])
  })

  it('sends nothing when the device refuses to save the removal, and says why', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('The quota has been exceeded.')
    })
    const send = vi.fn(sent)
    const { result } = renderHook(() => useArchived())

    await expect(archiveDinner('d1', send, neverAsked)).rejects.toThrow(
      'this device could not save the removal, so it was not sent: The quota has been exceeded.',
    )
    expect(send).not.toHaveBeenCalled()
    expect([...knownArchived(result.current)]).toEqual([])
    expect(marks()).toEqual({})
  })

  it('keeps it marked removing, hidden after a reload, when the device refuses to mark it archived', async () => {
    const setItem = Storage.prototype.setItem
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key: string, value: string) {
      if (value === ARCHIVED) throw new Error('The quota has been exceeded.')
      setItem.call(this, key, value)
    })
    const told = vi.fn()
    const stop = onArchived(told)

    await archiveDinner('d1', sent, neverAsked)
    stop()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
    expect(told).not.toHaveBeenCalled()

    resetArchived()
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  it('takes the mark away when the server refuses the removal', async () => {
    const { result } = renderHook(() => useArchived())
    const refusal = new ApiError(500, 'internal', 'internal message', true)

    await expect(archiveDinner('d1', () => Promise.reject(refusal), neverAsked)).rejects.toBe(refusal)
    expect(marks()).toEqual({})
    expect([...knownArchived(result.current)]).toEqual([])
  })

  it('marks archived a dinner the server no longer has (404)', async () => {
    const gone = new ApiError(404, 'not_found', 'dinner not found', false)

    await expect(archiveDinner('d1', () => Promise.reject(gone), neverAsked)).rejects.toBe(gone)
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('keeps a removal no answer settled hidden, says so, and asks the server at once', async () => {
    const ask = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    const { result } = renderHook(() => useArchived())

    const removal = archiveDinner('d1', () => Promise.reject(new TypeError('Failed to fetch')), ask)
    await expect(removal).rejects.toThrow(UnconfirmedRemovalError)
    await expect(removal).rejects.toThrow(
      'Failed to fetch. The server may have removed it, so this device hides it until the server can say whether it did.',
    )
    expect(ask).toHaveBeenCalledWith('d1')
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  it('reports a mark saved on this device that MealQuest did not save', () => {
    localStorage.setItem(archivedKey('d1'), 'true')
    const { result } = renderHook(() => useArchived())
    expect(() => knownArchived(result.current)).toThrow(
      'The removed dinner saved on this device (mealquest.archived-dinner.d1) is not one MealQuest saved.',
    )
  })
})

describe('checkRemovals', () => {
  it('takes away the mark of a dinner the server still has and marks archived one it archived', async () => {
    localStorage.setItem(archivedKey('d1'), REMOVING)
    localStorage.setItem(archivedKey('d2'), REMOVING)
    localStorage.setItem(archivedKey('d3'), ARCHIVED)
    const told = vi.fn()
    const stop = onArchived(told)
    const ask = vi.fn((id: string) => Promise.resolve(id === 'd1'))
    const { result } = renderHook(() => useArchived())

    await act(() => checkRemovals(ask))
    stop()
    expect(ask.mock.calls.map(([id]) => id).sort()).toEqual(['d1', 'd2'])
    expect(marks()).toEqual({ [archivedKey('d2')]: ARCHIVED, [archivedKey('d3')]: ARCHIVED })
    expect([...knownArchived(result.current)].sort()).toEqual(['d2', 'd3'])
    expect(told).toHaveBeenCalledWith('d2')
  })

  it('leaves a dinner marked removing when the server cannot be asked', async () => {
    localStorage.setItem(archivedKey('d1'), REMOVING)

    await checkRemovals(() => Promise.reject(new Error('answered from the copy saved on this device')))
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
  })

  it('leaves alone a removal this page is still sending', async () => {
    let answer!: () => void
    const removal = archiveDinner('d1', () => new Promise<void>((resolve) => (answer = resolve)), neverAsked)
    const ask = vi.fn(() => Promise.resolve(true))

    await checkRemovals(ask)
    expect(ask).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })

    answer()
    await removal
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })
})

describe('useArchived', () => {
  it('takes in a dinner another tab of the app marked', () => {
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)]).toEqual([])

    localStorage.setItem(archivedKey('d3'), ARCHIVED)
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: archivedKey('d3'), newValue: ARCHIVED }))
    })
    expect([...knownArchived(result.current)]).toEqual(['d3'])
  })

  it('keeps both dinners two tabs remove at once, before either hears of the other', async () => {
    // Two tabs are two pages: each its own copy of the module, over the one
    // localStorage of the device.
    vi.resetModules()
    const tabA = await import('./archived.ts')
    vi.resetModules()
    const tabB = await import('./archived.ts')
    expect(tabA.archiveDinner).not.toBe(tabB.archiveDinner)
    const inA = renderHook(() => tabA.useArchived())
    const inB = renderHook(() => tabB.useArchived())

    // Both remove a dinner while neither has had a storage event: tab A
    // first, then tab B, whose answer comes first, so tab A, which has not
    // heard of tab B's dinner, saves last.
    let answerA!: () => void
    let answerB!: () => void
    const inFlightA = tabA.archiveDinner('d1', () => new Promise<void>((resolve) => (answerA = resolve)), neverAsked)
    const inFlightB = tabB.archiveDinner('d2', () => new Promise<void>((resolve) => (answerB = resolve)), neverAsked)
    await act(async () => {
      answerB()
      await inFlightB
      answerA()
      await inFlightA
    })
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED, [archivedKey('d2')]: ARCHIVED })

    // Then the storage events arrive, each tab hearing of the other's.
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: archivedKey('d2'), newValue: ARCHIVED }))
      window.dispatchEvent(new StorageEvent('storage', { key: archivedKey('d1'), newValue: ARCHIVED }))
    })
    expect([...tabA.knownArchived(inA.result.current)].sort()).toEqual(['d1', 'd2'])
    expect([...tabB.knownArchived(inB.result.current)].sort()).toEqual(['d1', 'd2'])

    // A new page knows both.
    vi.resetModules()
    const reloaded = await import('./archived.ts')
    const fresh = renderHook(() => reloaded.useArchived())
    expect([...reloaded.knownArchived(fresh.result.current)].sort()).toEqual(['d1', 'd2'])
  })
})
