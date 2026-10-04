import { expect } from 'vitest'

// Minimum tap target from the handoff's accessibility rules.
export const MIN_HIT_AREA = 44

function px(value: string): number {
  return value.endsWith('px') ? Number.parseFloat(value) : 0
}

// The tap target an element's stylesheet guarantees: the larger of its
// declared size and declared minimum size, per axis. jsdom does no layout, so
// this reads the CSS Modules' computed values (Vitest runs with css: true).
// A dimension the CSS leaves unsized counts as 0 and fails the check.
export function hitArea(element: Element): { width: number; height: number } {
  const style = getComputedStyle(element)
  return {
    width: Math.max(px(style.width), px(style.minWidth)),
    height: Math.max(px(style.height), px(style.minHeight)),
  }
}

export function expectMinHitArea(element: Element) {
  const { width, height } = hitArea(element)
  expect(width, 'hit area width').toBeGreaterThanOrEqual(MIN_HIT_AREA)
  expect(height, 'hit area height').toBeGreaterThanOrEqual(MIN_HIT_AREA)
}
