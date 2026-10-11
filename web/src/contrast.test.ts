// Text contrast over the design tokens (docs/A11Y.md, "Text contrast"). Every
// colour a stylesheet gives text must be a token listed in TEXT_GROUNDS, and
// every pair listed there must reach WCAG AA's 4.5:1 for normal-size text.
import { describe, expect, it } from 'vitest'
import tokens from './styles/tokens.css?raw'

const stylesheets = import.meta.glob<string>(['./**/*.css', '!./styles/tokens.css'], {
  query: '?raw',
  import: 'default',
  eager: true,
})

// The grounds MealQuest's pages, cards, rows and tinted fills use.
const LIGHT_GROUNDS = [
  '--color-bg',
  '--color-surface',
  '--color-neutral-100',
  '--color-neutral-200',
  '--color-accent-100',
  '--color-accent-200',
  '--color-accent-2-100',
  '--color-accent-2-200',
]

// Each text colour token, with every ground it is drawn on.
const TEXT_GROUNDS: Record<string, string[]> = {
  '--color-text': [...LIGHT_GROUNDS, '--color-accent-2-300'],
  '--color-neutral-700': LIGHT_GROUNDS,
  '--color-neutral-800': LIGHT_GROUNDS,
  '--color-neutral-900': LIGHT_GROUNDS,
  '--color-accent': LIGHT_GROUNDS,
  '--color-accent-700': LIGHT_GROUNDS,
  '--color-accent-800': LIGHT_GROUNDS,
  '--color-accent-2-800': LIGHT_GROUNDS,
  '--color-accent-2-900': [...LIGHT_GROUNDS, '--color-accent-2-300'],
  // Light ink on the grape fills (buttons, the active nav pill, the chosen
  // text size) and on the dark ones (the Offline pill).
  '--on-accent': ['--color-accent', '--color-accent-800', '--color-neutral-800'],
  // The dark pill button and the chosen settings tab.
  '--color-bg': ['--color-text', '--color-neutral-800'],
  // The bottom bar's inactive items, on the dark bar.
  '--color-neutral-400': ['--color-text'],
}

function hex(name: string): string {
  const value = new RegExp(`^\\s*${name}:\\s*(#[0-9a-fA-F]{6})\\b`, 'm').exec(tokens)?.[1]
  if (value === undefined) throw new Error(`${name} is not a hex colour in tokens.css`)
  return value
}

// Relative luminance, per WCAG 2.
function luminance(colour: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = Number.parseInt(colour.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
}

function contrast(a: string, b: string): number {
  const [light, dark] = [luminance(hex(a)), luminance(hex(b))].sort((x, y) => y - x)
  return (light! + 0.05) / (dark! + 0.05)
}

describe('text contrast', () => {
  const pairs = Object.entries(TEXT_GROUNDS).flatMap(([text, grounds]) => grounds.map((ground) => [text, ground] as const))

  it.each(pairs)('%s on %s reaches 4.5:1', (text, ground) => {
    expect(contrast(text, ground)).toBeGreaterThanOrEqual(4.5)
  })

  it('gives text only the colour tokens checked above', () => {
    expect(Object.keys(stylesheets).length).toBeGreaterThan(0)
    const unchecked = Object.entries(stylesheets).flatMap(([path, css]) =>
      Array.from(css.matchAll(/(?<![\w-])color:\s*([^;}]+)/g))
        .map((match) => match[1]!.trim())
        .filter((value) => value !== 'inherit')
        .filter((value) => {
          const token = /^var\((--[\w-]+)\)$/.exec(value)?.[1]
          return token === undefined || !(token in TEXT_GROUNDS)
        })
        .map((value) => `${path}: color: ${value}`),
    )
    expect(unchecked).toEqual([])
  })
})
