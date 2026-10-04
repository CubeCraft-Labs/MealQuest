import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { declaredStyle } from '../test/cssRules.ts'
import { Photo } from './Photo.tsx'

const id = '01JABCDEFGHJKMNPQRSTVWXYZ0'

describe('Photo', () => {
  it('offers the 400 and 1200 renditions as a srcset', () => {
    render(<Photo id={id} name="Korean beef bowls" />)
    const img = screen.getByRole('img', { name: 'Korean beef bowls' })
    expect(img).toHaveAttribute('src', `/media/photos/${id}-400.jpg`)
    expect(img.getAttribute('srcset')!.split(', ')).toEqual([
      `/media/photos/${id}-400.jpg 400w`,
      `/media/photos/${id}-1200.jpg 1200w`,
    ])
  })

  it('loads lazily with the dinner name as alt text', () => {
    render(<Photo id={id} name="Pizza night" />)
    const img = screen.getByRole('img')
    expect(img).toHaveAttribute('loading', 'lazy')
    expect(img).toHaveAttribute('alt', 'Pizza night')
  })

  it('applies the washed filter', () => {
    render(<Photo id={id} name="Pizza night" />)
    expect(declaredStyle(screen.getByRole('img')).filter).toBe(
      'saturate(0.6) contrast(0.85) brightness(1.1) opacity(0.94)',
    )
  })

  it('takes its radius and inset from props', () => {
    render(<Photo id={id} name="Pizza night" radius="var(--radius-lg)" inset="10px" />)
    const img = screen.getByRole('img')
    const style = declaredStyle(img)
    expect(style.borderRadius).toBe('var(--photo-radius)')
    expect(style.margin).toBe('var(--photo-inset)')
    expect(img.style.getPropertyValue('--photo-radius')).toBe('var(--radius-lg)')
    expect(img.style.getPropertyValue('--photo-inset')).toBe('10px')
  })

  it('shows the striped placeholder when there is no photo', () => {
    const { container } = render(<Photo name="Beef tacos" />)
    expect(container.querySelector('img')).toBeNull()
    const placeholder = screen.getByRole('img', { name: 'Beef tacos' })
    expect(placeholder).toHaveTextContent('dish photo')
    expect(declaredStyle(placeholder).background.replace(/\s+/g, ' ')).toBe(
      'repeating-linear-gradient( 135deg, var(--color-neutral-300) 0 8px, var(--color-neutral-200) 8px 16px )',
    )
  })

  it('falls back to the placeholder when the image fails to load', () => {
    const { container } = render(<Photo id={id} name="Beef tacos" />)
    fireEvent.error(container.querySelector('img')!)
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByRole('img', { name: 'Beef tacos' })).toHaveTextContent('dish photo')
  })

  it('tries again when given a new id after a failure', () => {
    const { container, rerender } = render(<Photo id={id} name="Beef tacos" />)
    fireEvent.error(container.querySelector('img')!)
    rerender(<Photo id="01JZZZZZZZZZZZZZZZZZZZZZZZ" name="Beef tacos" />)
    expect(container.querySelector('img')).toHaveAttribute('src', '/media/photos/01JZZZZZZZZZZZZZZZZZZZZZZZ-400.jpg')
  })
})
