import { fireEvent, render, screen } from '@testing-library/react'
import { createRef } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Button } from './Button.tsx'
import styles from './Button.module.css'

describe('Button', () => {
  it('is a button that defaults to type="button" and the primary variant', () => {
    render(<Button>Save changes</Button>)
    const button = screen.getByRole('button', { name: 'Save changes' })
    expect(button).toHaveAttribute('type', 'button')
    expect(button).toHaveClass(styles.button!, styles.primary!)
  })

  it.each(['primary', 'outline', 'dark'] as const)('renders the %s variant with a ≥ 44 px hit area', (variant) => {
    render(<Button variant={variant}>Go</Button>)
    const button = screen.getByRole('button', { name: 'Go' })
    expect(button).toHaveClass(styles[variant]!)
    expectMinHitArea(button)
  })

  it('declares the 100 ms pressed scale transition', () => {
    render(<Button>Go</Button>)
    expect(getComputedStyle(screen.getByRole('button')).transition).toContain('transform 100ms')
  })

  it('does not fire onClick when disabled', () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        Go
      </Button>,
    )
    const button = screen.getByRole('button', { name: 'Go' })
    expect(button).toBeDisabled()
    fireEvent.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('forwards the ref, rest props and an explicit type', () => {
    const ref = createRef<HTMLButtonElement>()
    render(
      <Button ref={ref} type="submit" aria-describedby="hint">
        Send
      </Button>,
    )
    const button = screen.getByRole('button', { name: 'Send' })
    expect(ref.current).toBe(button)
    expect(button).toHaveAttribute('type', 'submit')
    expect(button).toHaveAttribute('aria-describedby', 'hint')
  })
})
