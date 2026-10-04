import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
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
})
