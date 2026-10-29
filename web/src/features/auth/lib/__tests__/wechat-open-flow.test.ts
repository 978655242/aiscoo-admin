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
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  createWeChatOpenLoginAttempt,
  currentWeChatOpenAttempt,
  invalidateWeChatOpenAttempt,
  weChatOpenProofHeaders,
} from '../wechat-open-flow'

beforeEach(() => {
  sessionStorage.clear()
  useAuthStore.getState().auth.reset('complete')
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: { request: vi.fn() },
  })
})
afterEach(() => {
  invalidateWeChatOpenAttempt()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset('complete')
})

describe('website WeChat browser-bound attempts', () => {
  it('keeps a validated return target and replaces the previous proof when reopened', async () => {
    let flow = 0
    const requests: unknown[] = []
    vi.spyOn(api, 'post').mockImplementation(async (_url, _data, config) => {
      requests.push(config?.headers)
      flow += 1
      return {
        data: {
          success: true,
          data: {
            flow_token: `flow-${flow}`,
            expires_at: Math.floor(Date.now() / 1000) + 600,
          },
        },
      }
    })
    const first = await createWeChatOpenLoginAttempt(
      '/xcode/authorize?flow_token=original'
    )
    expect(first.redirect).toBe('/xcode/authorize?flow_token=original')
    const second = await createWeChatOpenLoginAttempt(
      'https://attacker.invalid/steal'
    )
    expect(second.redirect).toBeUndefined()
    expect(second.proof).not.toBe(first.proof)
    expect(requests[0]).toEqual({ 'X-OAuth-Browser-Proof': first.proof })
    expect(() => weChatOpenProofHeaders(first.state)).toThrow(
      'no longer active'
    )
    expect(weChatOpenProofHeaders(second.state)).toEqual({
      'X-OAuth-Browser-Proof': second.proof,
    })
    invalidateWeChatOpenAttempt(first.state)
    expect(currentWeChatOpenAttempt(second.state)).toEqual(second)
  })

  it('rejects the old flow after identity changes even when the original anonymous state returns', async () => {
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          flow_token: 'owner-bound',
          expires_at: Math.floor(Date.now() / 1000) + 600,
        },
      },
    })
    const attempt = await createWeChatOpenLoginAttempt()
    useAuthStore.getState().auth.setUser({ id: 7, username: 'other', role: 1 })
    useAuthStore.getState().auth.reset('complete')
    expect(() => weChatOpenProofHeaders(attempt.state)).toThrow(
      'no longer active'
    )
  })

  it('fails before creating server state if browser locks or sessionStorage are unavailable', async () => {
    const request = vi.spyOn(api, 'post')
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: undefined,
    })
    await expect(createWeChatOpenLoginAttempt()).rejects.toThrow(
      'unavailable in this browser'
    )
    expect(request).not.toHaveBeenCalled()
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request: vi.fn() },
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied')
    })
    await expect(createWeChatOpenLoginAttempt()).rejects.toThrow(
      'Unable to store'
    )
    expect(request).not.toHaveBeenCalled()
  })
})
