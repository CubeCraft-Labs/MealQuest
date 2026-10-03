import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { ListRow } from './ListRow.tsx'
import { Toggle } from './Toggle.tsx'

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

  it('keeps a trailing toggle outside the row button so each control fires only its own action', () => {
    const onClick = vi.fn()
    const onCheckedChange = vi.fn()
    render(
      <ListRow
        title="Dinner reminders"
        meta="Push at 4 pm"
        trailing={<Toggle aria-label="Dinner reminders" checked={false} onCheckedChange={onCheckedChange} />}
        onClick={onClick}
      />,
    )
    const row = screen.getByRole('button', { name: /^Dinner reminders/ })
    const toggle = screen.getByRole('switch', { name: 'Dinner reminders' })
    expect(row).not.toContainElement(toggle)
    expect(row.querySelector('button')).toBeNull()

    fireEvent.click(toggle)
    expect(onCheckedChange).toHaveBeenCalledExactlyOnceWith(true)
    expect(onClick).not.toHaveBeenCalled()

    fireEvent.click(row)
    expect(onClick).toHaveBeenCalledOnce()
    expect(onCheckedChange).toHaveBeenCalledOnce()
  })
})
