import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Checkbox } from './Checkbox.tsx'

describe('Checkbox', () => {
  it('is a native checkbox named by its label', () => {
    render(<Checkbox label="Chicken thighs" />)
    const box = screen.getByRole('checkbox', { name: 'Chicken thighs' })
    expect(box.tagName).toBe('INPUT')
    expect(box).toHaveAttribute('type', 'checkbox')
    expect(box).not.toBeChecked()
  })

  it('toggles when the input or its label text is clicked', () => {
    render(<Checkbox label="Limes" />)
    const box = screen.getByRole('checkbox', { name: 'Limes' })
    fireEvent.click(box)
    expect(box).toBeChecked()
    fireEvent.click(screen.getByText('Limes'))
    expect(box).not.toBeChecked()
  })

  it('reflects a controlled checked state and disabled', () => {
    render(<Checkbox label="Rice" checked disabled readOnly />)
    const box = screen.getByRole('checkbox', { name: 'Rice' })
    expect(box).toBeChecked()
    expect(box).toBeDisabled()
  })

  it('has a ≥ 44 px hit area (the wrapping label)', () => {
    render(<Checkbox label="Rice" />)
    const hit = screen.getByRole('checkbox', { name: 'Rice' }).closest('label')!
    expectMinHitArea(hit)
  })
})
