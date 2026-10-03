import { render, screen } from '@testing-library/react'
import { House } from 'lucide-react'
import { describe, expect, it } from 'vitest'
import { Icon } from './Icon.tsx'

function svg(container: HTMLElement) {
  return container.querySelector('svg')!
}

describe('Icon', () => {
  it('is hidden and unnamed without a label', () => {
    const { container } = render(<Icon icon={House} />)
    expect(svg(container)).toHaveAttribute('aria-hidden', 'true')
    expect(svg(container)).not.toHaveAttribute('aria-label')
    expect(screen.queryByRole('img')).toBeNull()
  })

  it('is a named image when given a label', () => {
    const { container } = render(<Icon icon={House} label="Home" />)
    expect(svg(container)).not.toHaveAttribute('aria-hidden')
    expect(screen.getByRole('img', { name: 'Home' })).toBe(svg(container))
  })

  it('draws at stroke width 2.75', () => {
    const { container } = render(<Icon icon={House} />)
    expect(svg(container)).toHaveAttribute('stroke-width', '2.75')
  })

  it('is 22 px at the nav size', () => {
    const { container } = render(<Icon icon={House} size="nav" />)
    const style = getComputedStyle(svg(container))
    expect([style.width, style.height]).toEqual(['22px', '22px'])
  })

  it('is 16 px inline by default', () => {
    const { container } = render(<Icon icon={House} />)
    const style = getComputedStyle(svg(container))
    expect([style.width, style.height]).toEqual(['16px', '16px'])
  })

  it('merges a caller className', () => {
    const { container } = render(<Icon icon={House} className="extra" />)
    expect(svg(container)).toHaveClass('extra')
  })
})
