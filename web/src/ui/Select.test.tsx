import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Select } from './Select.tsx'

describe('Select', () => {
  it('is a native select named by its label', () => {
    const onChange = vi.fn()
    render(
      <Select label="Provider" value="a" onChange={onChange}>
        <option value="a">Alpha</option>
        <option value="b">Beta</option>
      </Select>,
    )
    const select = screen.getByRole('combobox', { name: 'Provider' })
    expect(select).toHaveValue('a')
    fireEvent.change(select, { target: { value: 'b' } })
    expect(onChange).toHaveBeenCalledOnce()
  })

  it('keeps a 44 px tap target', () => {
    render(
      <Select label="Model" defaultValue="a">
        <option value="a">Alpha</option>
      </Select>,
    )
    expectMinHitArea(screen.getByRole('combobox', { name: 'Model' }))
  })

  it('hides its chevron from assistive technology', () => {
    const { container } = render(
      <Select label="Model" defaultValue="a">
        <option value="a">Alpha</option>
      </Select>,
    )
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
  })
})
