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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import axios from 'axios'
import { Check } from 'lucide-react'
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { UserAvatar } from '@/components/user-avatar'
import { logout } from '@/features/auth/api'
import { AuthLayout } from '@/features/auth/auth-layout'
import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import {
  clearAuthenticatedClientState,
  resolveAuthentication,
} from '@/lib/auth-session'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

const requestSchema = z.object({
  schema_version: z.literal(1),
  server_time: z.number().int().positive(),
  client_id: z.enum(['xcode-desktop', 'xwork-cli']),
  expires_at: z.number().int().positive(),
})
const completeSchema = z.object({
  schema_version: z.literal(1),
  server_time: z.number().int().positive(),
  redirect_url: z.string(),
})

function validatedCallback(
  value: string,
  clientID: 'xcode-desktop' | 'xwork-cli'
): string {
  const url = new URL(value)
  const keys = [...url.searchParams.keys()]
  // 桌面与服务端共用四个精确回调约定；新增品牌 scheme 不放宽 CLI 回环边界。
  const registered =
    clientID === 'xwork-cli'
      ? /^http:\/\/127\.0\.0\.1:[1-9]\d{0,4}\/oauth\/callback\?/.test(value) &&
        Number(url.port || '80') <= 65535
      : [
          'aiscoo://oauth/callback',
          'aiscoo-dev://oauth/callback',
          'aiscoo-preview://oauth/callback',
          'zcode://oauth/callback',
        ].includes(value.slice(0, value.indexOf('?'))) &&
        url.host === 'oauth' &&
        url.pathname === '/callback'
  if (
    !registered ||
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

type Phase =
  | 'idle'
  | 'confirm'
  | 'switching'
  | 'submitting'
  | 'opening'
  | 'failed'
  | 'cancelled'
  | 'unavailable'

export function XCodeAuthorize(props: { flowToken: string }) {
  return (
    <AuthorizationRequest key={props.flowToken} flowToken={props.flowToken} />
  )
}

function AuthorizationRequest(props: { flowToken: string }) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const sessionID = useAuthStore((state) => state.auth.session?.sid)
  const [phase, setPhase] = useState<Phase>('idle')
  const [switchError, setSwitchError] = useState(false)
  const [expired, setExpired] = useState(false)
  const interaction = useRef({
    generation: 0,
    mounted: false,
    phase: 'idle' as Phase,
  })
  const confirmation = useRef<number | null>(null)
  const switchingToSignIn = useRef(false)
  const busy = useRef(false)
  const completion = useRef<AbortController | null>(null)

  function changePhase(value: Phase) {
    interaction.current.phase = value
    setPhase(value)
  }

  useLayoutEffect(() => {
    const currentInteraction = interaction.current
    currentInteraction.mounted = true
    // XCODE: 同步失效交互，避免 effect 清理前或 A→B→A 时复活旧授权/退出。
    const unsubscribe = useAuthStore.subscribe((state, previous) => {
      if (
        state.auth.user?.id === previous.auth.user?.id &&
        state.auth.session?.sid === previous.auth.session?.sid
      ) {
        return
      }
      interaction.current.generation += 1
      // XCODE: 身份变化须在刷新鉴权的 await 内同步取消，阻止旧授权进入 Axios adapter。
      completion.current?.abort()
      confirmation.current = null
      setSwitchError(false)
      if (interaction.current.phase === 'cancelled') return
      changePhase(
        ['submitting', 'opening', 'failed', 'unavailable'].includes(
          interaction.current.phase
        )
          ? 'unavailable'
          : 'idle'
      )
    })
    return () => {
      unsubscribe()
      currentInteraction.mounted = false
      currentInteraction.generation += 1
      completion.current?.abort()
    }
  }, [])
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
          // React Query owns deduplication and cancellation for this key. The
          // HTTP client's shared promise can still belong to an aborted mount.
          disableDuplicate: true,
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
  const action = useMutation({
    mutationFn: async ({
      kind,
      generation,
      userID,
      sid,
      signal,
    }: {
      kind: 'switch' | 'complete'
      generation: number
      userID: number
      sid: string
      signal?: AbortSignal
    }) => {
      const owner = useAuthStore.getState().auth
      if (
        !interaction.current.mounted ||
        interaction.current.generation !== generation
      ) {
        return
      }
      if (!request.data || Date.now() >= request.data.deadline) {
        changePhase('idle')
        setExpired(true)
        return
      }
      if (
        owner.user?.id !== userID ||
        owner.session?.sid !== sid ||
        (kind === 'switch' && confirmation.current !== generation)
      ) {
        changePhase('unavailable')
        return
      }
      const isCurrent = () =>
        interaction.current.mounted &&
        interaction.current.generation === generation &&
        useAuthStore.getState().auth.user?.id === userID &&
        useAuthStore.getState().auth.session?.sid === sid
      if (kind === 'switch') {
        // XCODE: 统一清理会清空授权 query，必须先捕获已验证的返回目标。
        const target = sanitizeAuthRedirect(
          `/xcode/authorize?${new URLSearchParams({ flow_token: props.flowToken })}`,
          window.location.origin
        )
        if (!target) {
          changePhase('unavailable')
          return
        }
        try {
          requireServerSuccess(await logout(sid))
          if (!isCurrent()) return
          switchingToSignIn.current = true
          clearAuthenticatedClientState(queryClient)
          void navigate({
            to: '/sign-in',
            search: { redirect: target },
            replace: true,
          })
        } catch (error) {
          if (!isCurrent()) return
          confirmation.current = null
          if (axios.isAxiosError(error) && error.response?.status === 409) {
            changePhase('unavailable')
          } else {
            setSwitchError(true)
            changePhase('idle')
          }
        }
        return
      }
      try {
        const response = await api.post<{ success: boolean; data: unknown }>(
          '/api/xcode/v1/auth/authorize/complete',
          { flow_token: props.flowToken },
          { singleUseAuthorization: true, skipErrorHandler: true, signal }
        )
        const result = completeSchema.parse(
          requireServerSuccess(response.data).data
        )
        const callback = validatedCallback(
          result.redirect_url,
          request.data.client_id
        )
        if (!isCurrent()) return
        changePhase('opening')
        location.assign(callback)
      } catch {
        if (isCurrent()) changePhase('failed')
      }
    },
    onSettled: () => {
      busy.current = false
      completion.current = null
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })

  function startAction(kind: 'switch' | 'complete') {
    if (
      busy.current ||
      interaction.current.phase !== (kind === 'switch' ? 'confirm' : 'idle') ||
      !request.data ||
      Date.now() >= request.data.deadline
    ) {
      return
    }
    const auth = useAuthStore.getState().auth
    if (
      !user ||
      !sessionID ||
      auth.user?.id !== user.id ||
      auth.session?.sid !== sessionID
    ) {
      return
    }
    busy.current = true
    changePhase(kind === 'switch' ? 'switching' : 'submitting')
    const controller = kind === 'complete' ? new AbortController() : null
    completion.current = controller
    action.mutate({
      kind,
      generation: interaction.current.generation,
      userID: user.id,
      sid: sessionID,
      signal: controller?.signal,
    })
  }

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
      switchingToSignIn.current ||
      Date.now() >= request.data.deadline ||
      user ||
      interaction.current.phase !== 'idle'
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

  let title: ReactNode
  let description: ReactNode
  let content: ReactNode
  if (phase === 'cancelled') {
    content = (
      <p role='status'>
        {t(
          'Authorization cancelled. You can close this page and return to aiscoo.'
        )}
      </p>
    )
  } else if (phase === 'unavailable') {
    content = (
      <ErrorState
        title={t('Account identity is temporarily unavailable')}
        description={t(
          'Reload this page to verify your account before continuing.'
        )}
      />
    )
  } else if (phase === 'failed') {
    content = (
      <ErrorState
        title={t('Authorization could not be completed')}
        description={t(
          'Start a new authorization request in aiscoo. This request will not be retried.'
        )}
      />
    )
  } else if (phase === 'submitting' || phase === 'opening') {
    content = (
      <div role='status'>
        <LoadingState
          message={
            phase === 'opening'
              ? t('Opening aiscoo...')
              : t('Authorizing aiscoo...')
          }
        />
      </div>
    )
  } else if (
    !props.flowToken ||
    request.isError ||
    expired ||
    (request.data && Date.now() >= request.data.deadline)
  ) {
    content = (
      <ErrorState
        title={t('Authorization request unavailable')}
        description={t('Start a new authorization request in aiscoo.')}
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
    const displayName = user.display_name?.trim() || user.username
    title = t('Authorize aiscoo')
    // CLI 与桌面使用不同客户端身份；授权页须如实说明被授权的应用。
    description =
      request.data.client_id === 'xwork-cli'
        ? t('Connect aiscoo CLI on this computer with your aiscoo account')
        : t('Connect the desktop app with your aiscoo account')
    content = (
      <div className='w-full space-y-6 text-start'>
        <section
          className='bg-muted/50 flex min-w-0 items-center gap-3 rounded-xl border p-4'
          aria-label={t('Current account')}
        >
          <UserAvatar
            userId={user.id}
            avatarUrl={user.avatar_url}
            className='size-11'
          />
          <div className='min-w-0 flex-1'>
            <h3 className='text-muted-foreground text-xs'>
              {t('Current account')}
            </h3>
            <p className='font-medium break-all'>{displayName}</p>
            <p className='text-muted-foreground text-sm break-all'>
              {user.username}
            </p>
          </div>
          <Button
            variant='link'
            className='h-auto shrink-0 px-0 whitespace-normal'
            disabled={phase !== 'idle' || action.isPending}
            onClick={() => {
              if (interaction.current.phase !== 'idle' || busy.current) return
              const auth = useAuthStore.getState().auth
              if (
                auth.user?.id !== user.id ||
                auth.session?.sid !== sessionID
              ) {
                return
              }
              confirmation.current = interaction.current.generation
              changePhase('confirm')
            }}
          >
            {t('Use another account')}
          </Button>
        </section>
        <section className='space-y-3 text-sm' aria-label={t('Permissions')}>
          <h3 className='text-base font-medium'>{t('Permissions')}</h3>
          <ul className='space-y-2.5'>
            {[
              t('Read your profile'),
              t('View your plans and usage'),
              t('Use your assigned aiscoo plan to call models'),
            ].map((permission) => (
              <li key={permission} className='flex items-start gap-2.5'>
                <span className='bg-primary/10 text-primary mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full'>
                  <Check className='size-3.5' aria-hidden='true' />
                </span>
                {permission}
              </li>
            ))}
          </ul>
          <div className='text-muted-foreground bg-muted/50 space-y-1.5 rounded-xl p-4 text-xs leading-5'>
            <p className='text-foreground'>
              {t(
                'aiscoo cannot manage payments, API keys or account security.'
              )}
            </p>
            <p className='text-foreground'>
              {t('Authorization does not automatically grant a plan.')}
            </p>
            <p>
              {t(
                'aiscoo connects using a separate device session. Your browser stays signed in.'
              )}
            </p>
            <p>
              {t(
                'The device session follows the native aiscoo session lifetime (30 days by default). You can revoke the aiscoo device in Login Sessions.'
              )}
            </p>
          </div>
        </section>
        <dl className='text-muted-foreground grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs'>
          <dt>{t('Application')}</dt>
          <dd className='text-foreground text-end break-all'>
            {request.data.client_id === 'xwork-cli'
              ? t('aiscoo client (CLI)')
              : t('aiscoo client (desktop)')}
          </dd>
          <dt>{t('This request expires at')}</dt>
          <dd className='text-foreground text-end'>
            <time dateTime={new Date(request.data.expires_at).toISOString()}>
              {new Date(request.data.expires_at).toLocaleString(locale)}
            </time>
          </dd>
        </dl>
        {switchError ? (
          <p role='alert' className='text-destructive text-sm'>
            {t(
              'Could not sign out. Your account has not been cleared. Try again.'
            )}
          </p>
        ) : null}
        {phase === 'switching' ? (
          <p role='status'>{t('Signing out...')}</p>
        ) : null}
        <div className='flex flex-col gap-3'>
          <Button
            className='h-auto min-h-11 w-full text-[15px] whitespace-normal shadow-[0_4px_8px_rgba(26,90,216,0.2)]'
            disabled={phase !== 'idle' || action.isPending}
            onClick={() => startAction('complete')}
          >
            {t('Authorize and open aiscoo')}
          </Button>
          <Button
            variant='ghost'
            className='text-muted-foreground min-h-11 w-full'
            disabled={phase !== 'idle' || action.isPending}
            onClick={() => {
              if (interaction.current.phase !== 'idle' || busy.current) return
              interaction.current.generation += 1
              changePhase('cancelled')
            }}
          >
            {t('Cancel')}
          </Button>
        </div>
        <ConfirmDialog
          open={phase === 'confirm' || phase === 'switching'}
          onOpenChange={(open) => {
            if (!open && interaction.current.phase === 'confirm') {
              confirmation.current = null
              changePhase('idle')
            }
          }}
          title={t('Switch login account?')}
          desc={
            <div className='space-y-3'>
              <p>
                {t(
                  'You will sign out of the current browser session for {{username}} and go to the aiscoo sign-in page.',
                  { username: user.username }
                )}
              </p>
              <p>
                {t(
                  'Other open aiscoo dashboard pages will also be affected by this account switch.'
                )}
              </p>
              <p>
                {t('After signing in, you must confirm authorization again.')}
              </p>
            </div>
          }
          cancelBtnText={t('Back')}
          confirmText={t('Sign out and switch')}
          isLoading={phase === 'switching'}
          handleConfirm={() => startAction('switch')}
        />
      </div>
    )
  }
  return (
    <AuthLayout title={title} description={description} wide>
      {content}
    </AuthLayout>
  )
}
