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
  RefusedRemovalError,
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
const internal = () => new ApiError(500, 'internal', 'internal message', true)

// marks is every dinner's mark saved on this device.
function marks() {
  return Object.fromEntries(Object.keys(localStorage).sort().map((key) => [key, localStorage.getItem(key)]))
}

// Server is the server's dinners as DELETE /api/v1/dinners/{id} archives
// them: an unconditional UPDATE, so archiving one again succeeds.
class Server {
  readonly archived = new Set<string>()

  // remove is a removal reaching the server: the dinner archived.
  remove(id: string) {
    this.archived.add(id)
  }
}

// Each way the server can answer a removal, or not answer it.
const ANSWERS: [string, () => Promise<unknown>][] = [
  ['archived', sent],
  ['not found (404)', () => Promise.reject(new ApiError(404, 'not_found', 'dinner not found', false))],
  ['refused (500)', () => Promise.reject(internal())],
  ['refused (409)', () => Promise.reject(new ApiError(409, 'conflict', 'conflict message', false))],
  ['not answered', () => Promise.reject(new TypeError('Failed to fetch'))],
]

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

  it('keeps a removal the server refuses hidden, says so, and sends it again at the next check', async () => {
    const { result } = renderHook(() => useArchived())
    const refusal = internal()
    const send = vi.fn((_id: string) => Promise.reject(refusal))

    const removal = archiveDinner('d1', send)
    await expect(removal).rejects.toThrow(RefusedRemovalError)
    await expect(removal).rejects.toThrow(
      'internal message. It could not be removed here; it stays hidden on this device until the server settles it.',
    )
    await expect(removal).rejects.toHaveProperty('cause', refusal)
    expect(send).toHaveBeenCalledTimes(1)
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })
    expect([...knownArchived(result.current)]).toEqual(['d1'])

    // The next check sends it again, and the server archives it.
    await act(() => checkRemovals(sent))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  // The rule that removes the race by construction: no answer, refusal or
  // read takes a mark away, so no later archive by another device has
  // anything to race against.
  it.each(ANSWERS)('never takes a mark away, whatever the server answers: %s', async (answerName, answer) => {
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem')
    const { result } = renderHook(() => useArchived())

    await archiveDinner('d1', answer).catch(() => undefined)
    localStorage.setItem(archivedKey('d2'), REMOVING)
    await act(() => checkRemovals(answer))
    // And a dinner marked archived is never marked removing again.
    localStorage.setItem(archivedKey('d3'), ARCHIVED)
    await archiveDinner('d3', answer).catch(() => undefined)
    await act(() => checkRemovals(answer))

    await waitFor(() => expect(deviceLocks().held(removalLock('d1'))).toBe(false))
    expect(removeItem).not.toHaveBeenCalled()
    const settled = answer === ANSWERS[0]![1] || answerName.startsWith('not found') ? ARCHIVED : REMOVING
    expect(marks()).toEqual({ [archivedKey('d1')]: settled, [archivedKey('d2')]: settled, [archivedKey('d3')]: ARCHIVED })
    expect([...knownArchived(result.current)]).toContain('d1')
    // A new page hides all three.
    resetArchived()
    const reloaded = renderHook(() => useArchived())
    expect([...knownArchived(reloaded.result.current)].sort()).toEqual(['d1', 'd2', 'd3'])
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

  it.each(['true', '', 'Archived', 'removing:0190b8c4-7e2a-4c1d-9f3e-2a1b3c4d5e6f'])(
    'reports a mark saved on this device that MealQuest did not save (%j)',
    (mark) => {
      localStorage.setItem(archivedKey('d1'), mark)
      const { result } = renderHook(() => useArchived())
      expect(() => knownArchived(result.current)).toThrow(
        'The removed dinner saved on this device (mealquest.archived-dinner.d1) is not one MealQuest saved.',
      )
    },
  )

  it('sends nothing for a dinner whose mark MealQuest did not save, and says why', async () => {
    localStorage.setItem(archivedKey('d1'), 'true')
    const send = vi.fn(sent)

    await expect(archiveDinner('d1', send)).rejects.toThrow(
      'The removed dinner saved on this device (mealquest.archived-dinner.d1) is not one MealQuest saved.',
    )
    expect(send).not.toHaveBeenCalled()
    expect(localStorage.getItem(archivedKey('d1'))).toBe('true')
  })
})

describe('checkRemovals', () => {
  it('sends each removal no answer settled again: one the server archives is marked archived, one it refuses stays marked removing', async () => {
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
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING, [archivedKey('d2')]: ARCHIVED, [archivedKey('d3')]: ARCHIVED })
    expect([...knownArchived(result.current)].sort()).toEqual(['d1', 'd2', 'd3'])
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

    await waitFor(() => expect(answer).toBeTypeOf('function'))
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
    // Tab B's own ApiError, as its client would throw.
    const { ApiError: TabApiError } = await import('../api/errors.ts')
    const refusedB = () => Promise.reject(new TabApiError(500, 'internal', 'internal message', true))
    return { tabA, tabB, refusedB }
  }

  it("does not settle a dinner while another tab's removal of it is unanswered, whatever the server would say", async () => {
    const { tabA, tabB, refusedB } = await twoTabs()
    let answerA!: () => void
    const inFlightA = tabA.archiveDinner('d1', () => new Promise<void>((resolve) => (answerA = resolve)))
    await waitFor(() => expect(answerA).toBeTypeOf('function'))

    // Tab B checks before tab A's removal commits: the server, which still
    // has the dinner, would refuse, but tab B sends nothing and the mark
    // stays.
    const sendB = vi.fn((_id: string) => refusedB())
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
    const sendA = vi.fn((_id: string) => new Promise<void>(() => undefined))
    void tabA.archiveDinner('d1', sendA)
    await waitFor(() => expect(sendA).toHaveBeenCalled())
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
    const sendA = vi.fn((_id: string) => new Promise<void>(() => undefined))
    void tabA.archiveDinner('d1', sendA)
    void tabA.archiveDinner('d2', sendA)
    await waitFor(() => expect(sendA).toHaveBeenCalledTimes(2))
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

    // Sent again, the server archives d1 and refuses d2: d1 is marked
    // archived, and d2 — which tab A's send, unanswered, may still archive —
    // stays hidden, marked removing, until the server archives it.
    const send = vi.fn((id: string) => (id === 'd2' ? Promise.reject(new PageApiError(500, 'internal', 'internal message', true)) : sent()))
    await act(() => reloaded.checkRemovals(send))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED, [archivedKey('d2')]: REMOVING })
    expect([...reloaded.knownArchived(page.result.current)].sort()).toEqual(['d1', 'd2'])
  })

  // A tab still showing a dinner another tab has archived.
  it('keeps archived, after a reload, a dinner a stale tab removes again and the server would refuse', async () => {
    const { tabA, tabB, refusedB } = await twoTabs()
    await tabA.archiveDinner('d1', sent)
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })

    // Tab B still shows d1 and removes it: the server would refuse, but it
    // is archived, so tab B sends nothing and the mark stays.
    const sendB = vi.fn((_id: string) => refusedB())
    await tabB.archiveDinner('d1', sendB)
    await tabB.checkRemovals(sendB)
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })

    vi.resetModules()
    const reloaded = await import('./archived.ts')
    const page = renderHook(() => reloaded.useArchived())
    expect([...reloaded.knownArchived(page.result.current)]).toEqual(['d1'])
  })

  // Tab B removes while tab A holds the lock settling the same dinner.
  it('does not mark removing again a dinner archived while a stale tab waited for its lock', async () => {
    const { tabA, tabB, refusedB } = await twoTabs()
    let answerA!: () => void
    const inFlightA = tabA.archiveDinner('d1', () => new Promise<void>((resolve) => (answerA = resolve)))
    await waitFor(() => expect(answerA).toBeTypeOf('function'))

    const sendB = vi.fn((_id: string) => refusedB())
    const inFlightB = tabB.archiveDinner('d1', sendB)
    await new Promise((resolve) => setTimeout(resolve, 20))
    answerA()
    await inFlightA
    await inFlightB
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  // A tab closes with its removal unanswered; the removal reaches the
  // server only after another tab's send of it again was refused.
  it('keeps hidden, after a reload, a removal from a closed tab that the server archives after refusing it sent again', async () => {
    const server = new Server()
    const { tabA, tabB, refusedB } = await twoTabs()
    let arriveLate!: () => void
    const sendA = vi.fn(
      (id: string) =>
        new Promise<void>(() => {
          arriveLate = () => server.remove(id)
        }),
    )
    void tabA.archiveDinner('d1', sendA)
    await waitFor(() => expect(sendA).toHaveBeenCalled())
    deviceLocks().close(removalLock('d1'))

    // Tab B sends it again; the server, busy, refuses that, and then tab A's
    // removal reaches it.
    const sendB = vi.fn((_id: string) => refusedB())
    await tabB.checkRemovals(sendB)
    expect(sendB.mock.calls).toEqual([['d1']])
    arriveLate()
    expect([...server.archived]).toEqual(['d1'])
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING })

    vi.resetModules()
    const reloaded = await import('./archived.ts')
    const page = renderHook(() => reloaded.useArchived())
    expect([...reloaded.knownArchived(page.result.current)]).toEqual(['d1'])

    // The next check sends it again; archiving again succeeds.
    await act(() => reloaded.checkRemovals(async (id) => server.remove(id)))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })
})

// F3 (review round 7): device A's removal R1 is settled only after device
// B's removal R2 has archived the dinner. Nothing device A learns from R1
// can show the dinner again: its mark is never taken away.
describe('a removal settled only after another device archived the dinner', () => {
  it.each([
    ['refused', () => Promise.reject(internal())],
    ['not answered', () => Promise.reject(new TypeError('Failed to fetch'))],
    ['archived', sent],
  ] as [string, () => Promise<unknown>][])('keeps the dinner hidden, after a reload too, when R1 is %s after R2 commits', async (_case, r1Answer) => {
    const server = new Server()
    const { result } = renderHook(() => useArchived())
    let settleR1!: () => void
    // R1's first send is answered only once R2 has committed; any send of
    // it again (at once, with no answer) is answered the same way.
    const send = vi.fn((_id: string) =>
      send.mock.calls.length > 1
        ? r1Answer()
        : new Promise<unknown>((resolve, reject) => {
            settleR1 = () => r1Answer().then(resolve, reject)
          }),
    )
    const r1 = archiveDinner('d1', send)
    await waitFor(() => expect(settleR1).toBeTypeOf('function'))

    // Device B's removal R2 commits: the server archives d1.
    server.remove('d1')
    expect([...server.archived]).toEqual(['d1'])
    // Only now does device A hear R1's answer.
    settleR1()
    await r1.catch(() => undefined)
    await waitFor(() => expect(deviceLocks().held(removalLock('d1'))).toBe(false))

    expect(localStorage.getItem(archivedKey('d1'))).not.toBeNull()
    expect([...knownArchived(result.current)]).toEqual(['d1'])
    resetArchived()
    const reloaded = renderHook(() => useArchived())
    expect([...knownArchived(reloaded.result.current)]).toEqual(['d1'])

    // Sent again, the server — which has it archived — archives it again
    // and answers: marked archived for good.
    await act(() => checkRemovals(async (id) => server.remove(id)))
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
