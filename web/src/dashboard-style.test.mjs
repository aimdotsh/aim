import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

test('dashboard metric icons keep an explicit centered grid box', () => {
  assert.match(css, /\.metric-grid \.metric-icon\s*\{[^}]*display:\s*grid[^}]*place-items:\s*center[^}]*\}/)
  assert.match(css, /\.metric-grid \.metric-icon svg\s*\{[^}]*width:\s*22px[^}]*height:\s*22px[^}]*\}/)
  assert.doesNotMatch(css, /\.metric-grid span\s*\{[^}]*display:\s*block/)
})
