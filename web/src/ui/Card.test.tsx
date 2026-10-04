import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Card } from './Card.tsx'
import styles from './Card.module.css'

describe('Card', () => {
  it('renders its children on the surface fill by default', () => {
    render(<Card aria-label="Tonight">Tacos</Card>)
    const card = screen.getByLabelText('Tonight')
    expect(card).toHaveTextContent('Tacos')
    expect(card).toHaveClass(styles.card!, styles.surface!)
  })

  it('supports the muted tone', () => {
    render(<Card data-testid="card" tone="muted" />)
    expect(screen.getByTestId('card')).toHaveClass(styles.muted!)
  })
})
