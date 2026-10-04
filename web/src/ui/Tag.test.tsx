import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Tag } from './Tag.tsx'
import styles from './Tag.module.css'

describe('Tag', () => {
  it('renders a static, non-interactive label', () => {
    render(<Tag>Draft</Tag>)
    const tag = screen.getByText('Draft')
    expect(tag).toHaveClass(styles.grape!)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('supports the teal tone', () => {
    render(<Tag tone="teal">Synced</Tag>)
    expect(screen.getByText('Synced')).toHaveClass(styles.teal!)
  })
})
