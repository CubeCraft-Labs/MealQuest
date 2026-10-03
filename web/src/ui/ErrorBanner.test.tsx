import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { declaredStyle } from '../test/cssRules.ts'
import { expectMinHitArea } from '../test/hitArea.ts'
import { ErrorBanner } from './ErrorBanner.tsx'

describe('ErrorBanner', () => {
  it('announces the message and calls onRetry from the Retry button', () => {
    const onRetry = vi.fn()
    render(<ErrorBanner onRetry={onRetry}>Couldn't load the Supply run.</ErrorBanner>)
    expect(screen.getByRole('alert')).toHaveTextContent("Couldn't load the Supply run.")
    const retry = screen.getByRole('button', { name: 'Retry' })
    expectMinHitArea(retry)
    fireEvent.click(retry)
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('uses a caller-supplied retry label', () => {
    render(
      <ErrorBanner onRetry={() => {}} retryLabel="Rebuild week">
        The plan didn't save.
      </ErrorBanner>,
    )
    expect(screen.getByRole('button', { name: 'Rebuild week' })).toBeInTheDocument()
  })

  it('is a neutral-100 card with grape-700 text', () => {
    render(<ErrorBanner onRetry={() => {}}>Offline</ErrorBanner>)
    expect(declaredStyle(screen.getByRole('alert')).background).toBe('var(--color-neutral-100)')
    expect(declaredStyle(screen.getByText('Offline')).color).toBe('var(--color-accent-700)')
  })
})
