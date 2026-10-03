import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { ListRow } from './ListRow.tsx'

describe('ListRow', () => {
  it('renders leading, title, meta and trailing slots without being interactive', () => {
    render(
      <ListRow
        leading={<span>icon</span>}
        title="Notifications"
        meta="Push and Google Chat"
        trailing={<span>chevron</span>}
      />,
    )
    for (const text of ['icon', 'Notifications', 'Push and Google Chat', 'chevron']) {
      expect(screen.getByText(text)).toBeInTheDocument()
    }
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('is a single button with a ≥ 44 px hit area when it has onClick', () => {
    const onClick = vi.fn()
    render(<ListRow title="10 new side quests to swipe" onClick={onClick} />)
    const row = screen.getByRole('button', { name: '10 new side quests to swipe' })
    expect(row).toHaveAttribute('type', 'button')
    expectMinHitArea(row)
    fireEvent.click(row)
    expect(onClick).toHaveBeenCalledOnce()
  })
})
