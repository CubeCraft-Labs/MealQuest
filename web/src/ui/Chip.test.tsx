import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Chip } from './Chip.tsx'

describe('Chip', () => {
  it('has no remove button unless onRemove is given', () => {
    render(<Chip label="Beef tacos" />)
    expect(screen.getByText('Beef tacos')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('exposes an accessible "Remove {label}" button that calls onRemove', () => {
    const onRemove = vi.fn()
    render(<Chip label="Beef tacos" onRemove={onRemove} />)
    const remove = screen.getByRole('button', { name: 'Remove Beef tacos' })
    expect(remove).toHaveAttribute('type', 'button')
    fireEvent.click(remove)
    expect(onRemove).toHaveBeenCalledOnce()
  })

  it('gives the remove button a ≥ 44 px hit area', () => {
    render(<Chip label="Pizza night" onRemove={() => {}} />)
    expectMinHitArea(screen.getByRole('button', { name: 'Remove Pizza night' }))
  })
})
