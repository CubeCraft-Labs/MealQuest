// Fails when a hex colour literal appears in any CSS or TypeScript file under
// web/ outside the design tokens: every colour must come from a custom
// property in tokens.css. Dependencies, build output and hidden directories
// (tool caches) are not source and are skipped.
import { readFileSync, readdirSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const allowed = new Set(['src/styles/tokens.css'])
const skippedDirs = new Set([join(webRoot, 'node_modules'), join(webRoot, 'dist')])
const extensions = new Set(['.css', '.ts', '.tsx'])
const hexColour = /#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b/g

function* sourceFiles(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (!skippedDirs.has(path) && !entry.name.startsWith('.')) yield* sourceFiles(path)
    } else if (extensions.has(extname(entry.name))) yield path
  }
}

const violations = []
for (const path of sourceFiles(webRoot)) {
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
