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
import axios, {
  type AxiosAdapter,
  type InternalAxiosRequestConfig,
} from 'axios'

import { useAuthStore } from '@/stores/auth-store'

const coordinatedAdapters = new WeakSet<AxiosAdapter>()

/** Receive an entire cookie-changing response before another tab changes identity. */
export function coordinateAuthCookieWrites(
  config: InternalAxiosRequestConfig
): void {
  const path = (config.url ?? '').split('?')[0]
  const changesCookie =
    path === '/api/user/auth/logout' ||
    path === '/api/user/auth/refresh' ||
    path === '/api/user/login' ||
    (path.startsWith('/api/user/login/') &&
      path !== '/api/user/login/encryption-key') ||
    path.startsWith('/api/user/passkey/login/') ||
    (/^\/api\/oauth\/[^/]+$/.test(path) && path !== '/api/oauth/state')
  if (!changesCookie || typeof navigator === 'undefined' || !navigator.locks) {
    return
  }

  const adapter = axios.getAdapter(config.adapter ?? axios.defaults.adapter)
  if (coordinatedAdapters.has(adapter)) return
  const owner = useAuthStore.getState().auth
  const ownerSID = owner.session?.sid
  const ownerUserID = owner.user?.id
  // 中文说明：只忽略迟到 body 无法阻止 Set-Cookie，锁必须覆盖底层完整响应；不能在响应拦截器刷新时继续持锁。
  const coordinated: AxiosAdapter = (request) =>
    navigator.locks.request(
      'new-api:auth-refresh',
      { mode: 'exclusive' },
      () => {
        const current = useAuthStore.getState().auth
        if (
          current.session?.sid !== ownerSID ||
          current.user?.id !== ownerUserID
        ) {
          throw new axios.CanceledError(
            'Authentication changed while waiting',
            request
          )
        }
        request.authCookieAdmission?.()
        if (request.signal?.aborted) {
          throw new axios.CanceledError(undefined, request)
        }
        return adapter(request)
      }
    )
  coordinatedAdapters.add(coordinated)
  config.adapter = coordinated
}
