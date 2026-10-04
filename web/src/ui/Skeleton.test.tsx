import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { animationKeyframes, declaredStyle, reducedMotionStyle } from '../test/cssRules.ts'
import { Skeleton } from './Skeleton.tsx'

function skeleton(container: HTMLElement) {
  return container.firstElementChild as HTMLElement
}

describe('Skeleton', () => {
  it('is a hidden pill in --color-surface', () => {
    const { container } = render(<Skeleton />)
    const element = skeleton(container)
    expect(element).toHaveAttribute('aria-hidden', 'true')
    const style = declaredStyle(element)
    expect(style.borderRadius).toBe('var(--radius-pill)')
    expect(style.background).toBe('var(--color-surface)')
  })

  it('pulses its opacity over 1.2 s', () => {
    const { container } = render(<Skeleton />)
    expect(declaredStyle(skeleton(container)).animation.split(' ').slice(1)).toEqual(['1.2s', 'ease-in-out', 'infinite'])
    expect(animationKeyframes(skeleton(container)).findRule('50%')?.style.opacity).toBe('0.45')
  })

  it('does not pulse under reduced motion', () => {
    const { container } = render(<Skeleton />)
    expect(reducedMotionStyle(skeleton(container)).animation).toBe('none')
  })

  it('merges a caller className and forwards style', () => {
    const { container } = render(<Skeleton className="line" style={{ width: '60%' }} />)
    expect(skeleton(container)).toHaveClass('line')
    expect(skeleton(container).style.width).toBe('60%')
  })
})
