// Fails when a hex colour literal appears in web/src outside the design
// tokens: every colour must come from a custom property in tokens.css.
import { readFileSync, readdirSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const srcDir = join(webRoot, 'src')
const allowed = new Set(['src/styles/tokens.css'])
const extensions = new Set(['.css', '.ts', '.tsx'])
const hexColour = /#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b/g

function* sourceFiles(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) yield* sourceFiles(path)
    else if (extensions.has(extname(entry.name))) yield path
  }
}

const violations = []
for (const path of sourceFiles(srcDir)) {
  const name = relative(webRoot, path).split('\\').join('/')
  if (allowed.has(name)) continue
  readFileSync(path, 'utf8')
    .split('\n')
    .forEach((line, i) => {
      for (const match of line.matchAll(hexColour)) {
        violations.push(`${name}:${i + 1}: ${match[0]}`)
      }
    })
}

if (violations.length > 0) {
  console.error('Hex colour literals found; use a token from src/styles/tokens.css:')
  for (const v of violations) console.error(`  ${v}`)
  process.exit(1)
}
console.log('lint:colors: no hex colour literals outside src/styles/tokens.css')
