// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.
//
// Privasys profile: what the assistant calls the person it works for, and
// their standing instructions.
//
// Both are the user's own settings (profile.json in their Drive folder, set
// from the user panel). The proxy keeps a copy in this worker's directory and
// names it in this plugin's config; the sections read it at every assembly,
// so a change applies to the next turn without a restart. The proxy has
// already refused what is not a name or text (control characters; for the
// name, braces and angle brackets too), and the sections are not
// interpolated. Each section says where it comes from in its first line, so
// the prompt a person reads in the chat's system-prompt row shows what is
// theirs without the harness editing dsh's view.

import { readFileSync } from 'node:fs'

export const name = 'privasys-profile'
export const inject = ['systemPrompt']

/** Right after the deployment's persona prefix (order 0): the name, then the instructions. */
const NAME_ORDER = 1
const INSTRUCTIONS_ORDER = 2

function profileIn(file) {
  try {
    const doc = JSON.parse(readFileSync(file, 'utf8'))
    return {
      name: typeof doc?.name === 'string' ? doc.name.trim() : '',
      instructions: typeof doc?.instructions === 'string' ? doc.instructions.trim() : '',
    }
  } catch {
    return { name: '', instructions: '' }
  }
}

export function apply(ctx, config) {
  const file = String(config?.file ?? '')
  if (file === '') return
  ctx.systemPrompt.section({
    name: 'privasys:profile',
    order: NAME_ORDER,
    interpolate: false,
    text: () => {
      const { name: person } = profileIn(file)
      return person === '' ? '' : `The person you work for asks to be called ${person}. Address them that way.`
    },
  })
  ctx.systemPrompt.section({
    name: 'privasys:instructions',
    order: INSTRUCTIONS_ORDER,
    interpolate: false,
    text: () => {
      const { instructions } = profileIn(file)
      if (instructions === '') return ''
      return `# From the person you work for (their standing instructions, set in their account)\n\n${instructions}`
    },
  })
}
