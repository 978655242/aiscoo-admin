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
import { api } from '@/lib/api'
import { AuthOperationError } from '@/lib/secure-verification'
import { getServerErrorMessageKey } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { VerificationOperation } from '../secure-verification/types'
import { sanitizeAuthRedirect } from './auth-redirect'
import { getAffiliateCode } from './storage'

export type WeChatOpenCapability = {
  enabled: boolean
  app_id: string
  redirect_uri: string
}

export type WeChatOpenAttempt = {
  state: string
  expiresAt: number
  proof: string
  intent: 'login' | 'bind' | 'verify'
  ownerSID: string | null
  ownerUserID: number | null
  redirect?: string
}

const attemptKey = 'wechat-open:current-attempt'

function readAttempt(): WeChatOpenAttempt | null {
  try {
    const raw = window.sessionStorage.getItem(attemptKey)
    if (!raw) return null
    const value: unknown = JSON.parse(raw)
    if (!value || typeof value !== 'object') return null
    const attempt = value as Partial<WeChatOpenAttempt>
    if (
      typeof attempt.state !== 'string' ||
      !attempt.state ||
      typeof attempt.proof !== 'string' ||
      !/^[A-Za-z0-9_-]{43}$/.test(attempt.proof) ||
      typeof attempt.expiresAt !== 'number' ||
      !Number.isFinite(attempt.expiresAt) ||
      !['login', 'bind', 'verify'].includes(attempt.intent ?? '') ||
      (attempt.ownerSID !== null && typeof attempt.ownerSID !== 'string') ||
      (attempt.ownerUserID !== null &&
        typeof attempt.ownerUserID !== 'number') ||
      (attempt.redirect !== undefined && typeof attempt.redirect !== 'string')
    ) {
      return null
    }
    return attempt as WeChatOpenAttempt
  } catch {
    return null
  }
}

export function invalidateWeChatOpenAttempt(state?: string): void {
  try {
    if (!state || readAttempt()?.state === state) {
      window.sessionStorage.removeItem(attemptKey)
    }
  } catch {
    // The owner check still rejects absent or unreadable attempt storage.
  }
}

// 当前标签在授权组件存活期间立即失效旧流程，A→B→A 也不能恢复先前 attempt。
useAuthStore.subscribe((state, previous) => {
  if (
    state.auth.session?.sid !== previous.auth.session?.sid ||
    state.auth.user?.id !== previous.auth.user?.id
  ) {
    invalidateWeChatOpenAttempt()
  }
})

export function currentWeChatOpenAttempt(state: string): WeChatOpenAttempt {
  const attempt = readAttempt()
  const owner = useAuthStore.getState().auth
  if (
    !attempt ||
    attempt.state !== state ||
    attempt.ownerSID !== (owner.session?.sid ?? null) ||
    attempt.ownerUserID !== (owner.user?.id ?? null)
  ) {
    throw new AuthOperationError(
      'WeChat login is no longer active. Please start again.'
    )
  }
  if (attempt.expiresAt <= Date.now()) {
    invalidateWeChatOpenAttempt(state)
    throw new AuthOperationError(
      'WeChat login flow expired. Please start again.'
    )
  }
  return attempt
}

export function weChatOpenProofHeaders(state: string): Record<string, string> {
  return { 'X-OAuth-Browser-Proof': currentWeChatOpenAttempt(state).proof }
}

export async function createWeChatOpenAuthorization(
  intent: WeChatOpenAttempt['intent'],
  operation?: VerificationOperation,
  signal?: AbortSignal,
  securityProof?: string,
  redirect?: string
): Promise<{ state: string; authorizationUrl?: string }> {
  if (
    typeof navigator === 'undefined' ||
    !navigator.locks ||
    !globalThis.crypto?.getRandomValues
  ) {
    throw new AuthOperationError(
      'Website WeChat login is unavailable in this browser.'
    )
  }
  invalidateWeChatOpenAttempt()
  const owner = useAuthStore.getState().auth
  const ownerSID = owner.session?.sid ?? null
  const ownerUserID = owner.user?.id ?? null
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  const proof = btoa(String.fromCharCode(...bytes))
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replaceAll('=', '')
  const marker = crypto.randomUUID()
  try {
    window.sessionStorage.setItem(attemptKey, marker)
    if (window.sessionStorage.getItem(attemptKey) !== marker) {
      throw new Error('Storage rejected')
    }
  } catch {
    throw new AuthOperationError(
      'Unable to store the WeChat login request. Please use account sign-in.'
    )
  }
  try {
    const response = await api.post(
      '/api/oauth/state',
      {
        provider: 'wechat-open',
        intent,
        aff: intent === 'login' ? getAffiliateCode() || undefined : undefined,
        scope: operation?.scope,
        ...(operation?.context ? { context: operation.context } : {}),
      },
      {
        headers: {
          'X-OAuth-Browser-Proof': proof,
          ...(securityProof ? { 'X-Security-Proof': securityProof } : {}),
        },
        skipAuthRefresh: intent === 'login',
        singleUseAuthorization: intent === 'bind',
        skipBusinessError: true,
        skipErrorHandler: true,
        signal,
      }
    )
    signal?.throwIfAborted()
    if (response.data?.success !== true) {
      throw new AuthOperationError(
        getServerErrorMessageKey(response.data) || 'Failed to initialize OAuth'
      )
    }
    const data = response.data.data
    if (
      typeof data?.flow_token !== 'string' ||
      !data.flow_token ||
      typeof data.expires_at !== 'number' ||
      !Number.isFinite(data.expires_at) ||
      data.expires_at * 1000 <= Date.now()
    ) {
      throw new AuthOperationError('Invalid WeChat login response.')
    }
    const currentOwner = useAuthStore.getState().auth
    if (
      window.sessionStorage.getItem(attemptKey) !== marker ||
      (currentOwner.session?.sid ?? null) !== ownerSID ||
      (currentOwner.user?.id ?? null) !== ownerUserID
    ) {
      throw new AuthOperationError(
        'WeChat login is no longer active. Please start again.'
      )
    }
    const attempt: WeChatOpenAttempt = {
      state: data.flow_token,
      expiresAt: data.expires_at * 1000,
      proof,
      intent,
      ownerSID,
      ownerUserID,
      redirect:
        sanitizeAuthRedirect(redirect, window.location.origin) ?? undefined,
    }
    window.sessionStorage.setItem(attemptKey, JSON.stringify(attempt))
    currentWeChatOpenAttempt(attempt.state)
    return { state: attempt.state, authorizationUrl: data.authorization_url }
  } catch (error) {
    try {
      if (window.sessionStorage.getItem(attemptKey) === marker) {
        window.sessionStorage.removeItem(attemptKey)
      }
    } catch {
      /* Storage failure cannot make a response current. */
    }
    throw error
  }
}

export async function createWeChatOpenLoginAttempt(
  redirect?: string,
  signal?: AbortSignal
): Promise<WeChatOpenAttempt> {
  const result = await createWeChatOpenAuthorization(
    'login',
    undefined,
    signal,
    undefined,
    redirect
  )
  return currentWeChatOpenAttempt(result.state)
}
