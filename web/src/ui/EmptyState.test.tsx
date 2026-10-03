import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { EmptyState } from './EmptyState.tsx'

describe('EmptyState', () => {
  it('shows a Bagel title and body without an action by default', () => {
    render(<EmptyState title="Quest log is empty">Swipe Try it on a Side quest to save it here.</EmptyState>)
    const title = screen.getByRole('heading', { name: 'Quest log is empty' })
    expect(getComputedStyle(title).fontFamily).toBe('var(--font-heading)')
    expect(screen.getByText('Swipe Try it on a Side quest to save it here.')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders the optional primary action and calls its handler', () => {
    const onClick = vi.fn()
    render(
      <EmptyState title="No Regulars yet" action={{ label: 'Add a dinner', onClick }}>
        Add the dinners you make most.
      </EmptyState>,
    )
    const action = screen.getByRole('button', { name: 'Add a dinner' })
    expectMinHitArea(action)
    fireEvent.click(action)
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
