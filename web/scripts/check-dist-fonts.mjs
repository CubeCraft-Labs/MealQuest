// Fails when the production build references Google Fonts: fonts are
// self-hosted, so dist must never point at fonts.googleapis.com or
// fonts.gstatic.com.
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const distDir = fileURLToPath(new URL('../dist', import.meta.url))
const hosts = ['fonts.googleapis.com', 'fonts.gstatic.com']

function* files(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) yield* files(path)
    else yield path
  }
}

const violations = []
for (const path of files(distDir)) {
  const content = readFileSync(path, 'latin1')
  for (const host of hosts) {
    if (content.includes(host)) violations.push(`${relative(distDir, path)}: ${host}`)
  }
}

if (violations.length > 0) {
  console.error('Production build references Google Fonts; fonts must be self-hosted:')
  for (const v of violations) console.error(`  ${v}`)
  process.exit(1)
}
console.log('check:dist-fonts: dist makes no Google Fonts requests')
