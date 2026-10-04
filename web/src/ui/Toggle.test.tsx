import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { hitArea, expectMinHitArea } from '../test/hitArea.ts'
import { Toggle } from './Toggle.tsx'

function Harness() {
  const [checked, setChecked] = useState(false)
  return <Toggle aria-label="Push: New weekly plan ready" checked={checked} onCheckedChange={setChecked} />
}

function toggle() {
  return screen.getByRole('switch', { name: 'Push: New weekly plan ready' })
}

describe('Toggle', () => {
  it('is a switch reflecting its state in aria-checked', () => {
    render(<Harness />)
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle())
    expect(toggle()).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(toggle())
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
  })

  it.each([' ', 'Enter'])('toggles with the %j key', (key) => {
    render(<Harness />)
    fireEvent.keyDown(toggle(), { key })
    expect(toggle()).toHaveAttribute('aria-checked', 'true')
    fireEvent.keyDown(toggle(), { key })
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
  })

  it('suppresses native activation for Space and Enter so a key press toggles once', () => {
    render(<Harness />)
    expect(fireEvent.keyDown(toggle(), { key: ' ' })).toBe(false)
    expect(fireEvent.keyUp(toggle(), { key: ' ' })).toBe(false)
    expect(fireEvent.keyDown(toggle(), { key: 'Enter' })).toBe(false)
    expect(fireEvent.keyDown(toggle(), { key: 'a' })).toBe(true)
  })

  it('ignores other keys', () => {
    const onCheckedChange = vi.fn()
    render(<Toggle aria-label="Chat" checked={false} onCheckedChange={onCheckedChange} />)
    fireEvent.keyDown(screen.getByRole('switch', { name: 'Chat' }), { key: 'a' })
    expect(onCheckedChange).not.toHaveBeenCalled()
  })

  it('is disabled when asked', () => {
    const onCheckedChange = vi.fn()
    render(<Toggle aria-label="Chat" checked={false} disabled onCheckedChange={onCheckedChange} />)
    const sw = screen.getByRole('switch', { name: 'Chat' })
    expect(sw).toBeDisabled()
    fireEvent.click(sw)
    expect(onCheckedChange).not.toHaveBeenCalled()
  })

  it('draws a 40×24 track inside a ≥ 44 px hit area', () => {
    render(<Harness />)
    expectMinHitArea(toggle())
    expect(hitArea(toggle().firstElementChild!)).toEqual({ width: 40, height: 24 })
  })
})
