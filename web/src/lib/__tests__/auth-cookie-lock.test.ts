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
import { once } from 'node:events'

import axios, { type AxiosRequestConfig } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { coordinateAuthCookieWrites } from '../auth-cookie-lock'

function browserLocks(): void {
  let pending = Promise.resolve<unknown>(undefined)
  vi.stubGlobal('navigator', {
    locks: {
      request: (
        _name: string,
        _options: unknown,
        action: () => Promise<unknown>
      ) => {
        const run = pending.then(action)
        pending = run.catch(() => undefined)
        return run
      },
    },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset('complete')
})

describe('authentication Cookie response order', () => {
  it('keeps the next login behind the entire previous response and rechecks queued admission', async () => {
    browserLocks()
    const network = new EventTarget()
    const firstStarted = once(network, 'started')
    const requests: string[] = []
    const client = axios.create({
      adapter: async (config) => {
        requests.push(config.url ?? '')
        if (config.url === '/api/oauth/wechat-open') {
          network.dispatchEvent(new Event('started'))
          await once(network, 'released')
        }
        return {
          data: { success: true },
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        }
      },
    })
    client.interceptors.request.use((config) => {
      coordinateAuthCookieWrites(config)
      return config
    })
    const first = client.get('/api/oauth/wechat-open')
    await firstStarted
    let cancelled = false
    const stale = client.post('/api/user/login', undefined, {
      authCookieAdmission: () => {
        if (cancelled) throw new Error('superseded login')
      },
    })
    const rejected = expect(stale).rejects.toThrow('superseded login')
    // Independent requests must remain usable while authentication is serialized.
    await client.get('/api/user/login/encryption-key')
    await client.get('/api/status')
    expect(requests).toEqual([
      '/api/oauth/wechat-open',
      '/api/user/login/encryption-key',
      '/api/status',
    ])
    cancelled = true
    network.dispatchEvent(new Event('released'))
    await first
    await rejected
    expect(requests).not.toContain('/api/user/login')
    await client.post('/api/user/login')
    expect(requests.at(-1)).toBe('/api/user/login')
  })

  it('releases the shared mutation lock after an HTTP failure without nesting on retry', async () => {
    browserLocks()
    let attempts = 0
    const client = axios.create({
      adapter: async (config) => {
        attempts += 1
        if (attempts === 1) {
          throw new axios.AxiosError('network failed', undefined, config)
        }
        return {
          data: { success: true },
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        }
      },
    })
    client.interceptors.request.use((config) => {
      coordinateAuthCookieWrites(config)
      return config
    })
    let failedConfig: AxiosRequestConfig | undefined
    try {
      await client.post('/api/user/login')
    } catch (error) {
      if (axios.isAxiosError(error)) failedConfig = error.config
    }
    if (!failedConfig) throw new Error('Expected a failed HTTP request')
    const result = await client.request(failedConfig)
    expect(result.status).toBe(200)
    expect(attempts).toBe(2)
  })

  it('cancels queued Cookie changes after the browser identity changes', async () => {
    browserLocks()
    useAuthStore.getState().auth.reset('complete')
    const network = new EventTarget()
    const started = once(network, 'started')
    const requests: string[] = []
    const client = axios.create({
      adapter: async (config) => {
        requests.push(config.url ?? '')
        network.dispatchEvent(new Event('started'))
        await once(network, 'released')
        return {
          data: { success: true },
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        }
      },
    })
    client.interceptors.request.use((config) => {
      coordinateAuthCookieWrites(config)
      return config
    })
    const first = client.get('/api/oauth/wechat-open')
    await started
    const queued = client.post('/api/user/login')
    const rejected = expect(queued).rejects.toThrow(
      'Authentication changed while waiting'
    )
    await Promise.resolve()
    useAuthStore.getState().auth.setUser({ id: 7, username: 'other', role: 1 })
    network.dispatchEvent(new Event('released'))
    await first
    await rejected
    expect(requests).toEqual(['/api/oauth/wechat-open'])
  })
})
