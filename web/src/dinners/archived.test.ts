import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ARCHIVED_KEY, knownArchived, recordArchived, resetArchived, useArchived } from './archived.ts'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('recordArchived', () => {
  it('saves each dinner archived on this device, so a new page knows it', () => {
    expect(recordArchived('d1')).toBeNull()
    expect(recordArchived('d2')).toBeNull()
    expect(JSON.parse(localStorage.getItem(ARCHIVED_KEY)!)).toEqual(['d1', 'd2'])

    resetArchived()
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)]).toEqual(['d1', 'd2'])
  })

  it('shows the dinner archived on this page even when the device refuses to save it, and says why', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('The quota has been exceeded.')
    })
    const { result } = renderHook(() => useArchived())

    let unsaved: Error | null = null
    act(() => {
      unsaved = recordArchived('d1')
    })
    expect(unsaved).toEqual(new Error('The quota has been exceeded.'))
    expect([...knownArchived(result.current)]).toEqual(['d1'])
    expect(localStorage.getItem(ARCHIVED_KEY)).toBeNull()
  })

  it('reports a record saved on this device that MealQuest did not save, and leaves it as it is', () => {
    localStorage.setItem(ARCHIVED_KEY, '{"d1":true}')
    const { result } = renderHook(() => useArchived())
    const unreadable = 'The archived dinners saved on this device (mealquest.archived-dinners) are not a list MealQuest saved.'
    expect(() => knownArchived(result.current)).toThrow(unreadable)

    let unsaved: Error | null = null
    act(() => {
      unsaved = recordArchived('d2')
    })
    expect(unsaved).toEqual(new Error(unreadable))
    expect(() => knownArchived(result.current)).toThrow(unreadable)
    expect(localStorage.getItem(ARCHIVED_KEY)).toBe('{"d1":true}')
  })
})

describe('useArchived', () => {
  it('takes in a dinner another tab of the app recorded archived', () => {
    const { result } = renderHook(() => useArchived())
    expect([...knownArchived(result.current)]).toEqual([])

    localStorage.setItem(ARCHIVED_KEY, '["d3"]')
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: ARCHIVED_KEY }))
    })
    expect([...knownArchived(result.current)]).toEqual(['d3'])
  })
})
