import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { declaredStyle } from '../test/cssRules.ts'
import { hitArea, expectMinHitArea } from '../test/hitArea.ts'
import { IconButton } from './IconButton.tsx'

describe('IconButton', () => {
  it('is named by its label, not its icon', () => {
    const onClick = vi.fn()
    render(
      <IconButton label="Notifications" onClick={onClick}>
        <svg />
      </IconButton>,
    )
    const button = screen.getByRole('button', { name: 'Notifications' })
    expect(button).toHaveAttribute('type', 'button')
    fireEvent.click(button)
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('keeps a 44 px hit area around the 40 px circle', () => {
    render(<IconButton label="Back" size={40} />)
    const button = screen.getByRole('button', { name: 'Back' })
    expectMinHitArea(button)
    const face = button.firstElementChild!
    expect(hitArea(face)).toEqual({ width: 40, height: 40 })
  })

  it('is 62 px at the large size', () => {
    render(<IconButton label="Try it" size={62} tone="teal" />)
    const button = screen.getByRole('button', { name: 'Try it' })
    expectMinHitArea(button)
    expect(hitArea(button)).toEqual({ width: 62, height: 62 })
  })

  it('shows its label as an 11 px caption under the circle, which names it', () => {
    render(
      <IconButton label="Pass" size={62} tone="neutral" showLabel>
        <svg />
      </IconButton>,
    )
    const button = screen.getByRole('button', { name: 'Pass' })
    expect(button).not.toHaveAttribute('aria-label')
    expect(button).toHaveTextContent('Pass')
    expect(getComputedStyle(button).fontSize).toBe('11px')
    expect(getComputedStyle(button).flexDirection).toBe('column')
    expectMinHitArea(button)
    const face = button.firstElementChild!
    expect(hitArea(face)).toEqual({ width: 62, height: 62 })
    expect(declaredStyle(face).background).toBe('var(--color-neutral-100)')
  })
})
