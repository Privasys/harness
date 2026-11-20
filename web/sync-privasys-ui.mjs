// Copies the shared Privasys header and footer stylesheet (@privasys/ui,
// libs/ui/src/styles.css in Privasys/websites) into vendor/privasys-ui.css,
// stamped with the websites commit it came from. The home page writes the
// same markup and class names as the shared Navbar and Footer components, so
// a change to the shared styles reaches it by running this script and
// committing the result: an update is always a reviewed diff, never a fetch
// at build time. When the styles are the same as the last sync, the file and
// its stamp are left alone, so a sync that changes nothing triggers no build.
//
//   node web/sync-privasys-ui.mjs <path to a Privasys/websites checkout>
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const websites = process.argv[2]
if (!websites) {
  console.error('usage: node web/sync-privasys-ui.mjs <websites checkout>')
  process.exit(2)
}
const rel = 'libs/ui/src/styles.css'
const git = (...args) => execFileSync('git', ['-C', websites, ...args], { encoding: 'utf8' }).trim()
if (git('status', '--porcelain', '--', rel)) {
  console.error(`${rel} has uncommitted changes in ${websites}: commit them first`)
  process.exit(1)
}
const commit = git('rev-parse', 'HEAD')
const lf = (text) => text.replace(/\r\n/g, '\n')
const css = lf(readFileSync(join(websites, rel), 'utf8'))
const out = join(here, 'vendor', 'privasys-ui.css')
const STAMP_LINES = 2
if (existsSync(out)) {
  const synced = lf(readFileSync(out, 'utf8')).split('\n').slice(STAMP_LINES).join('\n')
  if (synced === css) {
    console.log(`[sync] vendor/privasys-ui.css unchanged (websites ${commit.slice(0, 8)} has the same styles)`)
    process.exit(0)
  }
}
writeFileSync(out,
  `/* Synced from Privasys/websites ${rel} at ${commit}.\n` +
  `   Do not edit here: change the shared file, then run web/sync-privasys-ui.mjs. */\n` +
  css)
console.log(`[sync] vendor/privasys-ui.css <- websites ${commit.slice(0, 8)}`)
