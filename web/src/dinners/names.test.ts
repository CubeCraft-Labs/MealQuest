import { describe, expect, it } from 'vitest'
import cases from './testdata/dedupe_cases.json'
import { normalizeName, similarNames } from './names.ts'

// The cases internal/ai/harness's tests run against Normalize and Similar.
describe('dinner names, as the server compares them', () => {
  it('has cases to run', () => {
    expect(cases.normalize.length).toBeGreaterThan(0)
    expect(cases.similar.length).toBeGreaterThan(0)
  })

  it.each(cases.normalize)('normalizes "$name" to "$want"', ({ name, want }) => {
    expect(normalizeName(name)).toBe(want)
  })

  it.each(cases.similar)('"$a" and "$b" similar: $want', ({ a, b, want }) => {
    expect(similarNames(a, b)).toBe(want)
    expect(similarNames(b, a)).toBe(want)
  })
})
