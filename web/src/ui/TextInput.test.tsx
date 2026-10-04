import { fireEvent, render, screen } from '@testing-library/react'
import { createRef } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { hitArea, expectMinHitArea } from '../test/hitArea.ts'
import { TextInput } from './TextInput.tsx'

describe('TextInput', () => {
  it('is a textbox named by its label', () => {
    const onChange = vi.fn()
    render(<TextInput label="Dinner name" placeholder="e.g. Chicken enchiladas" onChange={onChange} />)
    const input = screen.getByRole('textbox', { name: 'Dinner name' })
    fireEvent.change(input, { target: { value: 'Chili' } })
    expect(onChange).toHaveBeenCalledOnce()
    expect(input).toHaveValue('Chili')
  })

  it('keeps the accessible name when the label is visually hidden', () => {
    render(<TextInput label="Add a regular" labelHidden />)
    expect(screen.getByRole('textbox', { name: 'Add a regular' })).toBeInTheDocument()
  })

  it('is a 48 px pill with a ≥ 44 px hit area', () => {
    render(<TextInput label="Dinner name" />)
    const input = screen.getByRole('textbox', { name: 'Dinner name' })
    expectMinHitArea(input)
    expect(hitArea(input).height).toBe(48)
  })

  it('forwards the ref', () => {
    const ref = createRef<HTMLInputElement>()
    render(<TextInput label="Dinner name" ref={ref} />)
    expect(ref.current).toBe(screen.getByRole('textbox', { name: 'Dinner name' }))
  })
})
