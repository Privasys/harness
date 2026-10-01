// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.
//
// Privasys profile: what the assistant calls the person it works for.
//
// The name is the user's own setting (profile.json in their Drive folder, set
// from the user panel). The proxy keeps a copy in this worker's directory and
// names it in this plugin's config; the prompt section reads it at every
// assembly, so a change applies to the next turn without a restart. The proxy
// has already refused anything that is not a name (control characters,
// braces, angle brackets), and the section is not interpolated.

import { readFileSync } from 'node:fs'

export const name = 'privasys-profile'
export const inject = ['systemPrompt']

/** Right after the deployment's persona prefix. */
const ORDER = 1

function nameIn(file) {
  try {
    const value = JSON.parse(readFileSync(file, 'utf8'))?.name
    return typeof value === 'string' ? value.trim() : ''
  } catch {
    return ''
  }
}

export function apply(ctx, config) {
  const file = String(config?.file ?? '')
  if (file === '') return
  ctx.systemPrompt.section({
    name: 'privasys:profile',
    order: ORDER,
    interpolate: false,
    text: () => {
      const person = nameIn(file)
      return person === '' ? '' : `The person you work for asks to be called ${person}. Address them that way.`
    },
  })
}
