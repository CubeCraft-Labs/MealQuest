import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors.ts'
import { deviceLocks } from '../test/locks.ts'
import {
  ARCHIVED,
  archivedKey,
  archiveDinner,
  checkRemovals,
  knownArchived,
  onArchived,
  removalLock,
  REMOVING,
  resetArchived,
  UnconfirmedRemovalError,
  useArchived,
} from './archived.ts'

afterEach(() => {
  vi.restoreAllMocks()
})

const sent = () => Promise.resolve(undefined)

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
    )
    await archiveDinner('d2', sent)
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

    await expect(archiveDinner('d1', send)).rejects.toThrow(
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

    await archiveDinner('d1', sent)
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

    await expect(archiveDinner('d1', () => Promise.reject(refusal))).rejects.toBe(refusal)
    expect(marks()).toEqual({})
    expect([...knownArchived(result.current)]).toEqual([])
  })

  it('marks archived a dinner the server no longer has (404)', async () => {
    const gone = new ApiError(404, 'not_found', 'dinner not found', false)

    await expect(archiveDinner('d1', () => Promise.reject(gone))).rejects.toBe(gone)
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('keeps a removal no answer settled hidden, says so, and sends it again at once', async () => {
    const send = vi.fn((_id: string) => Promise.reject(new TypeError('Failed to fetch')))
    const { result } = renderHook(() => useArchived())

    const removal = archiveDinner('d1', send)
    await expect(removal).rejects.toThrow(UnconfirmedRemovalError)
    await expect(removal).rejects.toThrow(
      'Failed to fetch. The server may have removed it, so this device hides it until the server can say whether it did.',
    )
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
    expect(send.mock.calls).toEqual([['d1'], ['d1']])
    await waitFor(() => expect(deviceLocks().held(removalLock('d1'))).toBe(false))
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  it('sends nothing and saves nothing in a browser with no Web Locks, and says why', async () => {
    Reflect.deleteProperty(navigator, 'locks')
    const send = vi.fn(sent)

    await expect(archiveDinner('d1', send)).rejects.toThrow(
      'this browser cannot keep its tabs from settling one removal at once (no Web Locks), so it was not sent',
    )
    expect(send).not.toHaveBeenCalled()
    expect(marks()).toEqual({})
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
  it('sends each removal no answer settled again: one the server archives is marked archived, one it refuses loses its mark', async () => {
    localStorage.setItem(archivedKey('d1'), REMOVING)
    localStorage.setItem(archivedKey('d2'), REMOVING)
    localStorage.setItem(archivedKey('d3'), ARCHIVED)
    const told = vi.fn()
    const stop = onArchived(told)
    const send = vi.fn((id: string) => (id === 'd1' ? Promise.reject(new ApiError(500, 'internal', 'internal message', true)) : sent()))
    const { result } = renderHook(() => useArchived())

    await act(() => checkRemovals(send))
    stop()
    expect(send.mock.calls.map(([id]) => id).sort()).toEqual(['d1', 'd2'])
    expect(marks()).toEqual({ [archivedKey('d2')]: ARCHIVED, [archivedKey('d3')]: ARCHIVED })
    expect([...knownArchived(result.current)].sort()).toEqual(['d2', 'd3'])
    expect(told).toHaveBeenCalledWith('d2')
  })

  it('marks archived a removal the server answers not found (404)', async () => {
    localStorage.setItem(archivedKey('d1'), REMOVING)

    await checkRemovals(() => Promise.reject(new ApiError(404, 'not_found', 'dinner not found', false)))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('leaves a dinner marked removing when the server does not answer', async () => {
    localStorage.setItem(archivedKey('d1'), REMOVING)

    await checkRemovals(() => Promise.reject(new TypeError('Failed to fetch')))
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
  })

  it('leaves alone a removal this page is still sending', async () => {
    let answer!: () => void
    const removal = archiveDinner('d1', () => new Promise<void>((resolve) => (answer = resolve)))
    const send = vi.fn(sent)

    const check = checkRemovals(send)
    await Promise.resolve()
    expect(send).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })

    answer()
    await removal
    await check
    expect(send).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })
})

describe('a removal in one tab and a check in another', () => {
  // Two tabs are two pages: each its own copy of the module, over the one
  // localStorage and the one Web Locks of the device.
  async function twoTabs() {
    vi.resetModules()
    const tabA = await import('./archived.ts')
    vi.resetModules()
    const tabB = await import('./archived.ts')
    expect(tabA.checkRemovals).not.toBe(tabB.checkRemovals)
    return { tabA, tabB }
  }

  it("does not settle a dinner while another tab's removal of it is unanswered, whatever the server would say", async () => {
    const { tabA, tabB } = await twoTabs()
    let answerA!: () => void
    const inFlightA = tabA.archiveDinner('d1', () => new Promise<void>((resolve) => (answerA = resolve)))

    // Tab B checks before tab A's removal commits: the server, which still
    // has the dinner, would refuse nothing yet, but tab B sends nothing and
    // the mark stays.
    const sendB = vi.fn((_id: string) => Promise.reject(new ApiError(500, 'internal', 'internal message', true)))
    const checkB = tabB.checkRemovals(sendB)
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })

    // Tab A's removal is answered: archived, and tab B's check, holding the
    // lock after it, finds it settled and sends nothing.
    answerA()
    await inFlightA
    await checkB
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('settles, in the tab still open, a removal whose tab closed before its answer, by sending it again', async () => {
    const { tabA, tabB } = await twoTabs()
    void tabA.archiveDinner('d1', () => new Promise<void>(() => undefined))
    const sendB = vi.fn(sent)
    const checkB = tabB.checkRemovals(sendB)
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(sendB).not.toHaveBeenCalled()

    // Tab A closes: the browser lets its lock go.
    deviceLocks().close(removalLock('d1'))
    await checkB
    expect(sendB.mock.calls).toEqual([['d1']])
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('after the only tab closed mid-removal, a reload hides the dinner and settles it either way', async () => {
    const { tabA } = await twoTabs()
    void tabA.archiveDinner('d1', () => new Promise<void>(() => undefined))
    void tabA.archiveDinner('d2', () => new Promise<void>(() => undefined))
    await Promise.resolve()
    deviceLocks().close(removalLock('d1'))
    deviceLocks().close(removalLock('d2'))

    // A new page: before anything is settled it hides both — no recipe link
    // for a dinner the server may have archived.
    vi.resetModules()
    const reloaded = await import('./archived.ts')
    // The new page's own ApiError, as its client would throw.
    const { ApiError: PageApiError } = await import('../api/errors.ts')
    const page = renderHook(() => reloaded.useArchived())
    expect([...reloaded.knownArchived(page.result.current)].sort()).toEqual(['d1', 'd2'])

    // Sent again, the server archives d1 and refuses d2, which it still has:
    // d1 stays hidden for good, d2 shows again.
    const send = vi.fn((id: string) => (id === 'd2' ? Promise.reject(new PageApiError(500, 'internal', 'internal message', true)) : sent()))
    await act(() => reloaded.checkRemovals(send))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
    expect([...reloaded.knownArchived(page.result.current)]).toEqual(['d1'])
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
    const inFlightA = tabA.archiveDinner('d1', () => new Promise<void>((resolve) => (answerA = resolve)))
    const inFlightB = tabB.archiveDinner('d2', () => new Promise<void>((resolve) => (answerB = resolve)))
    // Each is sent once its tab holds the dinner's lock.
    await waitFor(() => {
      expect(answerA).toBeTypeOf('function')
      expect(answerB).toBeTypeOf('function')
    })
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
