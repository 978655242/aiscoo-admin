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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { AuthLayout } from '@/features/auth/auth-layout'
import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { resolveAuthentication } from '@/lib/auth-session'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

const requestSchema = z.object({
  schema_version: z.literal(1),
  server_time: z.number().int().positive(),
  client_id: z.literal('xcode-desktop'),
  expires_at: z.number().int().positive(),
})
const completeSchema = z.object({
  schema_version: z.literal(1),
  server_time: z.number().int().positive(),
  redirect_url: z.string(),
})

function validatedCallback(value: string): string {
  const url = new URL(value)
  const keys = [...url.searchParams.keys()]
  if (
    !value.startsWith('zcode://oauth/callback?') ||
    url.protocol !== 'zcode:' ||
    url.host !== 'oauth' ||
    url.pathname !== '/callback' ||
    url.username ||
    url.password ||
    url.hash ||
    keys.length !== 3 ||
    new Set(keys).size !== 3 ||
    !keys.every((key) => ['code', 'state', 'provider'].includes(key)) ||
    url.searchParams.get('provider') !== 'new-api' ||
    !url.searchParams.get('code')?.trim() ||
    !url.searchParams.get('state')?.trim()
  ) {
    throw new Error('Invalid authorization callback')
  }
  return value
}

export function XCodeAuthorize(props: { flowToken: string }) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const user = useAuthStore((state) => state.auth.user)
  const sessionID = useAuthStore((state) => state.auth.session?.sid)
  const [confirmation, setConfirmation] = useState(false)
  const [expired, setExpired] = useState(false)
  const attempted = useRef(false)
  const confirmedSession = useRef<string | undefined>(undefined)
  const request = useQuery({
    queryKey: ['xcode', 'authorization-request', props.flowToken],
    queryFn: async ({ signal }) => {
      const response = await api.get<{ success: boolean; data: unknown }>(
        '/api/xcode/v1/auth/authorize/request',
        {
          params: { flow_token: props.flowToken },
          signal,
          skipAuthRefresh: true,
          skipErrorHandler: true,
        }
      )
      const view = requestSchema.parse(requireServerSuccess(response.data).data)
      if (view.expires_at <= view.server_time) {
        throw new Error('Authorization request expired')
      }
      const deadline = Date.now() + view.expires_at - view.server_time
      const authentication = await resolveAuthentication()
      if (
        authentication.kind === 'transient_error' ||
        authentication.kind === 'out_of_sync'
      ) {
        throw new Error('Native authentication unavailable')
      }
      return { ...view, deadline }
    },
    enabled: Boolean(props.flowToken),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    meta: { errorToast: false },
  })
  const complete = useMutation({
    mutationFn: async () => {
      const currentSession = useAuthStore.getState().auth.session?.sid
      if (
        !request.data ||
        Date.now() >= request.data.deadline ||
        !currentSession ||
        currentSession !== confirmedSession.current
      ) {
        throw new Error('Authorization request unavailable')
      }
      const response = await api.post<{ success: boolean; data: unknown }>(
        '/api/xcode/v1/auth/authorize/complete',
        { flow_token: props.flowToken },
        { singleUseAuthorization: true, skipErrorHandler: true }
      )
      const result = completeSchema.parse(
        requireServerSuccess(response.data).data
      )
      const callback = validatedCallback(result.redirect_url)
      if (useAuthStore.getState().auth.session?.sid !== currentSession) {
        throw new Error('Native session changed')
      }
      location.assign(callback)
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })

  useEffect(() => {
    if (!request.data) return
    const timeout = window.setTimeout(
      () => setExpired(true),
      Math.max(0, request.data.deadline - Date.now())
    )
    return () => window.clearTimeout(timeout)
  }, [request.data])

  useEffect(() => {
    if (
      !request.data ||
      expired ||
      Date.now() >= request.data.deadline ||
      user ||
      attempted.current
    ) {
      return
    }
    const target = sanitizeAuthRedirect(
      `/xcode/authorize?${new URLSearchParams({ flow_token: props.flowToken })}`,
      window.location.origin
    )
    if (target) {
      void navigate({
        to: '/sign-in',
        search: { redirect: target },
        replace: true,
      })
    }
  }, [request.data, expired, user, navigate, props.flowToken])

  let content: ReactNode
  if (complete.isError) {
    content = (
      <ErrorState
        title={t('Authorization could not be completed')}
        description={t(
          'Start a new authorization request in XCode. This request will not be retried.'
        )}
      />
    )
  } else if (complete.isPending || complete.isSuccess) {
    content = (
      <div role='status'>
        <LoadingState
          message={
            complete.isSuccess
              ? t('Opening XCode...')
              : t('Authorizing XCode...')
          }
        />
      </div>
    )
  } else if (!props.flowToken || request.isError || expired) {
    content = (
      <ErrorState
        title={t('Authorization request unavailable')}
        description={t('Start a new authorization request in XCode.')}
      />
    )
  } else if (!request.data || !user || !sessionID) {
    content = (
      <div role='status'>
        <LoadingState message={t('Checking authorization request...')} />
      </div>
    )
  } else {
    const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
    content = (
      <div className='w-full space-y-6'>
        <div className='space-y-2'>
          <h2 className='text-2xl font-semibold tracking-tight'>
            {t('Authorize XCode')}
          </h2>
          <p className='text-muted-foreground text-sm'>
            {t(
              'XCode will connect to your New API account using a separate device session. Your browser stays signed in.'
            )}
          </p>
        </div>
        <dl className='grid gap-3 text-sm'>
          <div>
            <dt className='text-muted-foreground'>{t('Application')}</dt>
            <dd className='font-medium'>{request.data.client_id}</dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Account Info')}</dt>
            <dd className='font-medium break-words'>{user.display_name}</dd>
            <dd className='break-all'>{user.username}</dd>
          </div>
          <div>
            <dt className='text-muted-foreground'>{t('Request expires at')}</dt>
            <dd>
              <time dateTime={new Date(request.data.expires_at).toISOString()}>
                {new Date(request.data.expires_at).toLocaleString(locale)}
              </time>
            </dd>
          </div>
        </dl>
        <Button
          className='w-full'
          onClick={() => {
            confirmedSession.current = sessionID
            setConfirmation(true)
          }}
        >
          {t('Authorize XCode')}
        </Button>
        <ConfirmDialog
          open={confirmation}
          onOpenChange={setConfirmation}
          title={t('Authorize XCode')}
          desc={
            <div className='space-y-3'>
              <p>
                {t('Authorize XCode to connect as {{username}}?', {
                  username: user.username,
                })}
              </p>
              <p>
                {t(
                  'XCode can read your profile, account plans and account usage, and run inference with your designated XCode plan.'
                )}
              </p>
              <p>
                {t(
                  'XCode cannot manage payments, API keys or account security.'
                )}
              </p>
              <p>
                {t(
                  'The device session follows the native New API session lifetime (30 days by default). You can revoke the XCode device in Login Sessions.'
                )}
              </p>
            </div>
          }
          confirmText={t('Authorize and open XCode')}
          handleConfirm={() => {
            if (attempted.current) return
            attempted.current = true
            setConfirmation(false)
            complete.mutate()
          }}
        />
      </div>
    )
  }
  return <AuthLayout>{content}</AuthLayout>
}
