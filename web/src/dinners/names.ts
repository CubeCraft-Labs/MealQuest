// The PWA's copy of the server's dinner-name rules (internal/ai/harness
// Normalize and Similar), so a screen can tell beforehand that the dinners
// API would refuse a name as a duplicate. The server still decides.
// internal/ai/harness/testdata/dedupe_cases.json is run against both.

// SIMILAR_THRESHOLD is harness.SimilarThreshold.
const SIMILAR_THRESHOLD = 0.85

// fillerWords is harness's fillerWords.
const fillerWords = new Set(['easy', 'best', 'homemade', 'quick', 'simple', 'the', 'a', 'recipe', 'classic'])

// letterFolds are the letters harness folds that canonical decomposition
// leaves whole: its ligatures and extra letters, and the common stroked
// Latin letters. Letters with accents fold by decomposing and dropping the
// marks, as harness's table does; rarer stroked letters are compared as
// they are.
const letterFolds: Record<string, string> = {
  ß: 'ss',
  æ: 'ae',
  œ: 'oe',
  þ: 'th',
  ð: 'd',
  ı: 'i',
  ŉ: 'n',
  ĳ: 'ij',
  ø: 'o',
  đ: 'd',
  ħ: 'h',
  ŀ: 'l',
  ł: 'l',
  ŧ: 't',
  ƀ: 'b',
  ƶ: 'z',
  ǥ: 'g',
  ɨ: 'i',
}

const combiningMark = /\p{Mn}/u
const wordChar = /[\p{L}\p{Nd}]/u

// normalizeName is harness.Normalize: lowercased, accents dropped, letters
// folded, apostrophes removed, other punctuation turned into spaces and
// filler words dropped. "Easy Chicken Tikka-Masala!" is
// "chicken tikka masala".
export function normalizeName(name: string): string {
  let out = ''
  for (const ch of name.toLowerCase().normalize('NFD')) {
    if (combiningMark.test(ch) || ch === "'" || ch === '’') continue
    const fold = letterFolds[ch]
    if (fold !== undefined) out += fold
    else if (wordChar.test(ch)) out += ch
    else out += ' '
  }
  return out
    .split(' ')
    .filter((word) => word !== '' && !fillerWords.has(word))
    .join(' ')
}

function words(name: string): Set<string> {
  const normalized = normalizeName(name)
  return new Set(normalized === '' ? [] : normalized.split(' '))
}

// similarNames is harness.Similar: whether a and b name the same dinner, by
// the share of the words of their normalized forms they have in common.
export function similarNames(a: string, b: string): boolean {
  const left = words(a)
  const right = words(b)
  if (left.size === 0 || right.size === 0) return false
  let shared = 0
  for (const word of left) if (right.has(word)) shared++
  return shared / (left.size + right.size - shared) >= SIMILAR_THRESHOLD
}
