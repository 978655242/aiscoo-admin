/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createHash } from 'node:crypto'

import { describe, expect, it } from 'vitest'

import { getUserAvatarDataUri, getUserAvatarSeed } from '../avatar'

describe('user avatar identity', () => {
  it.each([42, '42'])(
    'uses the shared namespace for canonical user ID %s',
    (id) => {
      expect(getUserAvatarSeed(id)).toBe('aiscoo:new-api:multiavatar:v1:42')
    }
  )

  it('preserves large decimal string IDs without converting them to numbers', () => {
    expect(getUserAvatarSeed('9223372036854775807')).toBe(
      'aiscoo:new-api:multiavatar:v1:9223372036854775807'
    )
  })

  it.each([
    null,
    undefined,
    '',
    '0',
    '042',
    ' 42 ',
    '+42',
    '-42',
    '42.0',
    '4.2e1',
    0,
    -1,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    Number.MAX_SAFE_INTEGER + 1,
  ])('uses no stable avatar for invalid identity %s', (id) => {
    expect(getUserAvatarSeed(id)).toBeNull()
    expect(getUserAvatarDataUri(id)).toBeNull()
  })
})

describe('local avatar generation', () => {
  it('matches the cross-client ID42 SVG golden with the default background', () => {
    const uri = getUserAvatarDataUri(42)
    expect(uri).toMatch(/^data:image\/svg\+xml;charset=utf-8,/)
    const svg = decodeURIComponent((uri ?? '').split(',')[1])
    expect(Buffer.byteLength(svg, 'utf8')).toBe(3038)
    expect(createHash('sha256').update(svg, 'utf8').digest('hex')).toBe(
      'f0d93b3eb893342c11ba3dfd82ac35b8e525bd4658437d8be1bd4ef1f3d91892'
    )
  })

  it('gives numeric and string identities the same image and separates different users', () => {
    expect(getUserAvatarDataUri(42)).toBe(getUserAvatarDataUri('42'))
    expect(getUserAvatarDataUri(43)).not.toBe(getUserAvatarDataUri(42))
  })
})
