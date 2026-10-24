import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors.ts'
import { deviceLocks } from '../test/locks.ts'
import { REMOVING_MARK } from '../test/removals.ts'
import {
  ARCHIVED,
  archivedKey,
  archiveDinner,
  checkRemovals,
  knownArchived,
  onArchived,
  removalLock,
  removingMark,
  resetArchived,
  UnconfirmedRemovalError,
  useArchived,
  type RemovalOutcome,
  type Removals,
} from './archived.ts'

afterEach(() => {
  vi.restoreAllMocks()
})

const sent = () => Promise.resolve(undefined)
const internal = () => new ApiError(500, 'internal', 'internal message', true)

// removals is how a test's removals reach the server: send made, unless
// given, and withdraw failing the test, unless given.
function removals(over: Partial<Removals> = {}): Removals {
  return {
    send: sent,
    withdraw: () => Promise.reject(new Error('the test did not expect a withdrawal')),
    ...over,
  }
}

const withdrawn = () => Promise.resolve<RemovalOutcome>('withdrawn')

// marks is every dinner's mark saved on this device; a removing mark reads
// as REMOVING_MARK's source, whatever its removal.
function marks() {
  return Object.fromEntries(
    Object.keys(localStorage)
      .sort()
      .map((key) => {
        const mark = localStorage.getItem(key)
        return [key, mark !== null && REMOVING_MARK.test(mark) ? 'removing:<id>' : mark]
      }),
  )
}
const REMOVING_ID = 'removing:<id>'

// removalOf is the removal id dinner id is marked removing with.
function removalOf(id: string): string {
  const mark = localStorage.getItem(archivedKey(id)) ?? ''
  if (!REMOVING_MARK.test(mark)) throw new Error(`${id} is not marked removing: ${mark}`)
  return mark.slice('removing:'.length)
}

// Server is the server's record of removals: each made (the dinner
// archived) or withdrawn once, for good, as dinner_removals records them.
class Server {
  readonly archived = new Set<string>()
  private outcomes = new Map<string, RemovalOutcome>()

  // arrive is a removal reaching the server: made, unless withdrawn.
  arrive(id: string, removal: string) {
    if (this.outcomes.get(removal) === 'withdrawn') throw new ApiError(409, 'removal_withdrawn', 'the removal was withdrawn', false)
    this.outcomes.set(removal, 'archived')
    this.archived.add(id)
  }

  withdraw(_id: string, removal: string): RemovalOutcome {
    const outcome = this.outcomes.get(removal) ?? 'withdrawn'
    this.outcomes.set(removal, outcome)
    return outcome
  }
}

describe('archiveDinner', () => {
  it('marks the dinner removing before the removal is sent, then archived, so a new page knows it', async () => {
    let markWhenSent: string | null = null
    let removalSent: string | null = null
    await archiveDinner(
      'd1',
      removals({
        send: (_id, removal) => {
          markWhenSent = localStorage.getItem(archivedKey('d1'))
          removalSent = removal
          return sent()
        },
      }),
    )
    await archiveDinner('d2', removals())
    expect(markWhenSent).toBe(removingMark(removalSent!))
    expect(markWhenSent).toMatch(REMOVING_MARK)
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

    await expect(archiveDinner('d1', removals({ send }))).rejects.toThrow(
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

    await archiveDinner('d1', removals())
    stop()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING_ID })
    expect(told).not.toHaveBeenCalled()

    resetArchived()
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  it('takes the mark away when the server refuses the removal and then withdraws it', async () => {
    const { result } = renderHook(() => useArchived())
    const refusal = internal()
    const withdraw = vi.fn(withdrawn)

    await expect(archiveDinner('d1', removals({ send: () => Promise.reject(refusal), withdraw }))).rejects.toBe(refusal)
    expect(withdraw).toHaveBeenCalledTimes(1)
    expect(marks()).toEqual({})
    expect([...knownArchived(result.current)]).toEqual([])
  })

  it('marks archived a removal the server refused to answer but, asked to withdraw it, says it made', async () => {
    const told = vi.fn()
    const stop = onArchived(told)

    await archiveDinner('d1', removals({ send: () => Promise.reject(internal()), withdraw: () => Promise.resolve('archived') }))
    stop()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
    expect(told).toHaveBeenCalledWith('d1')
  })

  it('withdraws the very removal it sent', async () => {
    const send = vi.fn((_id: string, _removal: string) => Promise.reject(internal()))
    const withdraw = vi.fn((_id: string, _removal: string) => withdrawn())

    await expect(archiveDinner('d1', removals({ send, withdraw }))).rejects.toThrow('internal message')
    expect(send.mock.calls[0]![1]).toMatch(/^[0-9a-f-]{36}$/)
    expect(withdraw.mock.calls).toEqual([['d1', send.mock.calls[0]![1]]])
  })

  it('marks archived a dinner the server no longer has (404)', async () => {
    const gone = new ApiError(404, 'not_found', 'dinner not found', false)

    await expect(archiveDinner('d1', removals({ send: () => Promise.reject(gone) }))).rejects.toBe(gone)
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('keeps a removal the server did not settle hidden, says so, and sends it again at once', async () => {
    const send = vi.fn((_id: string, _removal: string) => Promise.reject(new TypeError('Failed to fetch')))
    const withdraw = vi.fn((_id: string, _removal: string) => Promise.reject(new TypeError('Failed to fetch')))
    const { result } = renderHook(() => useArchived())

    const removal = archiveDinner('d1', removals({ send, withdraw }))
    await expect(removal).rejects.toThrow(UnconfirmedRemovalError)
    await expect(removal).rejects.toThrow(
      'Failed to fetch. The server may have removed it, so this device hides it until the server can say whether it did.',
    )
    await waitFor(() => expect(withdraw).toHaveBeenCalledTimes(2))
    // Sent again with the same removal, so the server makes it at most once.
    const id = removalOf('d1')
    expect(send.mock.calls).toEqual([
      ['d1', id],
      ['d1', id],
    ])
    await waitFor(() => expect(deviceLocks().held(removalLock('d1'))).toBe(false))
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING_ID })
    expect([...knownArchived(result.current)]).toEqual(['d1'])
  })

  it('sends nothing and saves nothing in a browser with no Web Locks, and says why', async () => {
    Reflect.deleteProperty(navigator, 'locks')
    const send = vi.fn(sent)

    await expect(archiveDinner('d1', removals({ send }))).rejects.toThrow(
      'this browser cannot keep its tabs from settling one removal at once (no Web Locks), so it was not sent',
    )
    expect(send).not.toHaveBeenCalled()
    expect(marks()).toEqual({})
  })

  it.each(['true', 'removing', 'removing:', 'removing:not-a-uuid', 'removing:0190B8C4-7E2A-4C1D-9F3E-2A1B3C4D5E6F'])(
    'reports a mark saved on this device that MealQuest did not save (%s)',
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

    await expect(archiveDinner('d1', removals({ send }))).rejects.toThrow(
      'The removed dinner saved on this device (mealquest.archived-dinner.d1) is not one MealQuest saved.',
    )
    expect(send).not.toHaveBeenCalled()
    expect(localStorage.getItem(archivedKey('d1'))).toBe('true')
  })
})

// A1 and A2 are removal ids saved before a check.
const A1 = '0190b8c4-7e2a-4c1d-9f3e-2a1b3c4d5e61'
const A2 = '0190b8c4-7e2a-4c1d-9f3e-2a1b3c4d5e62'

describe('checkRemovals', () => {
  it('sends each removal the server did not settle again: one it makes is marked archived, one it refuses and withdraws loses its mark', async () => {
    localStorage.setItem(archivedKey('d1'), removingMark(A1))
    localStorage.setItem(archivedKey('d2'), removingMark(A2))
    localStorage.setItem(archivedKey('d3'), ARCHIVED)
    const told = vi.fn()
    const stop = onArchived(told)
    const send = vi.fn((id: string, _removal: string) => (id === 'd1' ? Promise.reject(internal()) : sent()))
    const withdraw = vi.fn((_id: string, _removal: string) => withdrawn())
    const { result } = renderHook(() => useArchived())

    await act(() => checkRemovals(removals({ send, withdraw })))
    stop()
    expect(send.mock.calls.sort()).toEqual([
      ['d1', A1],
      ['d2', A2],
    ])
    expect(withdraw.mock.calls).toEqual([['d1', A1]])
    expect(marks()).toEqual({ [archivedKey('d2')]: ARCHIVED, [archivedKey('d3')]: ARCHIVED })
    expect([...knownArchived(result.current)].sort()).toEqual(['d2', 'd3'])
    expect(told).toHaveBeenCalledWith('d2')
  })

  it('marks archived a removal the server answers not found (404)', async () => {
    localStorage.setItem(archivedKey('d1'), removingMark(A1))

    await checkRemovals(removals({ send: () => Promise.reject(new ApiError(404, 'not_found', 'dinner not found', false)) }))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('marks archived a removal whose withdrawal finds the dinner gone (404)', async () => {
    localStorage.setItem(archivedKey('d1'), removingMark(A1))

    await checkRemovals(
      removals({
        send: () => Promise.reject(internal()),
        withdraw: () => Promise.reject(new ApiError(404, 'not_found', 'dinner not found', false)),
      }),
    )
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it.each([
    ['no answer', () => Promise.reject(new TypeError('Failed to fetch'))],
    ['refused', () => Promise.reject(internal())],
    ['no outcome', () => Promise.reject(new Error('POST /api/v1/dinners/{id}/removals/{removal}/withdraw: response is not an outcome'))],
  ])('leaves a refused removal marked removing when its withdrawal is not settled either (%s)', async (_case, withdraw) => {
    localStorage.setItem(archivedKey('d1'), removingMark(A1))

    await checkRemovals(removals({ send: () => Promise.reject(internal()), withdraw }))
    expect(localStorage.getItem(archivedKey('d1'))).toBe(removingMark(A1))
  })

  it('leaves a dinner marked removing when the server does not answer', async () => {
    localStorage.setItem(archivedKey('d1'), removingMark(A1))

    await checkRemovals(
      removals({ send: () => Promise.reject(new TypeError('Failed to fetch')), withdraw: () => Promise.reject(new TypeError('Failed to fetch')) }),
    )
    expect(localStorage.getItem(archivedKey('d1'))).toBe(removingMark(A1))
  })

  it('leaves alone a removal this page is still sending', async () => {
    let answer!: () => void
    const removal = archiveDinner('d1', removals({ send: () => new Promise<void>((resolve) => (answer = resolve)) }))
    const send = vi.fn(sent)

    const check = checkRemovals(removals({ send }))
    await Promise.resolve()
    expect(send).not.toHaveBeenCalled()
    await waitFor(() => expect(answer).toBeTypeOf('function'))
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING_ID })

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
    const inFlightA = tabA.archiveDinner('d1', removals({ send: () => new Promise<void>((resolve) => (answerA = resolve)) }))
    await waitFor(() => expect(answerA).toBeTypeOf('function'))

    // Tab B checks before tab A's removal commits: the server, which still
    // has the dinner, would refuse, but tab B sends nothing and the mark
    // stays.
    const sendB = vi.fn((_id: string, _removal: string) => refusedB())
    const withdrawB = vi.fn(withdrawn)
    const checkB = tabB.checkRemovals(removals({ send: sendB, withdraw: withdrawB }))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: REMOVING_ID })

    // Tab A's removal is answered: archived, and tab B's check, holding the
    // lock after it, finds it settled and sends nothing.
    answerA()
    await inFlightA
    await checkB
    expect(sendB).not.toHaveBeenCalled()
    expect(withdrawB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('settles, in the tab still open, a removal whose tab closed before its answer, by sending it again', async () => {
    const { tabA, tabB } = await twoTabs()
    const sendA = vi.fn((_id: string, _removal: string) => new Promise<void>(() => undefined))
    void tabA.archiveDinner('d1', removals({ send: sendA }))
    await waitFor(() => expect(sendA).toHaveBeenCalled())
    const sendB = vi.fn((_id: string, _removal: string) => sent())
    const checkB = tabB.checkRemovals(removals({ send: sendB }))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(sendB).not.toHaveBeenCalled()

    // Tab A closes: the browser lets its lock go.
    deviceLocks().close(removalLock('d1'))
    await checkB
    expect(sendB.mock.calls).toEqual([['d1', sendA.mock.calls[0]![1]]])
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  it('after the only tab closed mid-removal, a reload hides the dinner and settles it either way', async () => {
    const { tabA } = await twoTabs()
    const sendA = vi.fn((_id: string, _removal: string) => new Promise<void>(() => undefined))
    void tabA.archiveDinner('d1', removals({ send: sendA }))
    void tabA.archiveDinner('d2', removals({ send: sendA }))
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

    // Sent again, the server makes d1 and refuses d2, which it still has and
    // then withdraws: d1 stays hidden for good, d2 shows again.
    const send = vi.fn((id: string) => (id === 'd2' ? Promise.reject(new PageApiError(500, 'internal', 'internal message', true)) : sent()))
    await act(() => reloaded.checkRemovals(removals({ send, withdraw: withdrawn })))
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
    expect([...reloaded.knownArchived(page.result.current)]).toEqual(['d1'])
  })

  // F1: a tab still showing a dinner another tab has archived.
  it('keeps archived, after a reload, a dinner a stale tab removes again and the server would refuse', async () => {
    const { tabA, tabB, refusedB } = await twoTabs()
    await tabA.archiveDinner('d1', removals())
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })

    // Tab B still shows d1 and removes it: the server would refuse, but it
    // is archived, so tab B sends nothing and the mark stays.
    const sendB = vi.fn((_id: string, _removal: string) => refusedB())
    const withdrawB = vi.fn(withdrawn)
    await tabB.archiveDinner('d1', removals({ send: sendB, withdraw: withdrawB }))
    expect(sendB).not.toHaveBeenCalled()
    expect(withdrawB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })

    // A check in tab B leaves it archived too.
    await tabB.checkRemovals(removals({ send: sendB, withdraw: withdrawB }))
    expect(sendB).not.toHaveBeenCalled()

    vi.resetModules()
    const reloaded = await import('./archived.ts')
    const page = renderHook(() => reloaded.useArchived())
    expect([...reloaded.knownArchived(page.result.current)]).toEqual(['d1'])
  })

  // F1: tab B removes while tab A holds the lock settling the same dinner.
  it('does not mark removing again a dinner archived while a stale tab waited for its lock', async () => {
    const { tabA, tabB, refusedB } = await twoTabs()
    let answerA!: () => void
    const inFlightA = tabA.archiveDinner('d1', removals({ send: () => new Promise<void>((resolve) => (answerA = resolve)) }))
    await waitFor(() => expect(answerA).toBeTypeOf('function'))

    const sendB = vi.fn((_id: string, _removal: string) => refusedB())
    const inFlightB = tabB.archiveDinner('d1', removals({ send: sendB, withdraw: withdrawn }))
    await new Promise((resolve) => setTimeout(resolve, 20))
    answerA()
    await inFlightA
    await inFlightB
    expect(sendB).not.toHaveBeenCalled()
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
  })

  // F2: a tab closes with its removal unanswered; the removal reaches the
  // server only after another tab's send of it again was refused.
  async function closedTabThenRefusedRetry(lateBeforeWithdrawal: boolean) {
    const server = new Server()
    const { tabA, tabB, refusedB } = await twoTabs()
    let arriveLate!: () => void
    const sendA = vi.fn(
      (id: string, removal: string) =>
        new Promise<void>(() => {
          arriveLate = () => server.arrive(id, removal)
        }),
    )
    void tabA.archiveDinner('d1', removals({ send: sendA }))
    await waitFor(() => expect(sendA).toHaveBeenCalled())
    // Tab A closes before its removal reaches the server.
    deviceLocks().close(removalLock('d1'))

    // Tab B sends it again; the server, busy, refuses that.
    const sendB = vi.fn((_id: string, _removal: string) => refusedB())
    const withdrawB = vi.fn(async (id: string, removal: string) => {
      if (lateBeforeWithdrawal) arriveLate()
      return server.withdraw(id, removal)
    })
    await tabB.checkRemovals(removals({ send: sendB, withdraw: withdrawB }))
    expect(sendB.mock.calls).toEqual([['d1', sendA.mock.calls[0]![1]]])
    expect(withdrawB.mock.calls).toEqual([['d1', sendA.mock.calls[0]![1]]])

    // Tab A's removal reaches the server now, if it has not yet.
    if (!lateBeforeWithdrawal) expect(() => arriveLate()).toThrow('the removal was withdrawn')

    vi.resetModules()
    const reloaded = await import('./archived.ts')
    const page = renderHook(() => reloaded.useArchived())
    return { server, hidden: [...reloaded.knownArchived(page.result.current)] }
  }

  it('marks archived, after a reload, a removal from a closed tab the server made after refusing it sent again', async () => {
    const { server, hidden } = await closedTabThenRefusedRetry(true)
    expect([...server.archived]).toEqual(['d1'])
    expect(marks()).toEqual({ [archivedKey('d1')]: ARCHIVED })
    expect(hidden).toEqual(['d1'])
  })

  it('shows again a dinner whose removal from a closed tab was withdrawn, and the server never makes it, however late it arrives', async () => {
    const { server, hidden } = await closedTabThenRefusedRetry(false)
    expect([...server.archived]).toEqual([])
    expect(marks()).toEqual({})
    expect(hidden).toEqual([])
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
    const inFlightA = tabA.archiveDinner('d1', removals({ send: () => new Promise<void>((resolve) => (answerA = resolve)) }))
    const inFlightB = tabB.archiveDinner('d2', removals({ send: () => new Promise<void>((resolve) => (answerB = resolve)) }))
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
