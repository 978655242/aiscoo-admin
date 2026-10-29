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
import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { IconWeChat } from '@/assets/brand-icons'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { useTheme } from '@/context/theme-provider'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import {
  createWeChatOpenLoginAttempt,
  invalidateWeChatOpenAttempt,
  type WeChatOpenAttempt,
  type WeChatOpenCapability,
} from '../lib/wechat-open-flow'

export type WxLoginOptions = {
  id: string
  appid: string
  scope: 'snsapi_login'
  redirect_uri: string
  state: string
  self_redirect: false
  style: 'black' | 'white'
  lang: 'cn' | 'en'
  onReady?: (ready: boolean) => void
  onQRcodeReady?: () => void
  onCleanup?: () => void
}

declare global {
  interface Window {
    WxLogin?: new (options: WxLoginOptions) => unknown
  }
}

const sdkURL = 'https://res.wx.qq.com/connect/zh_CN/htmledition/js/wxLogin.js'

function loadWxLogin(
  signal: AbortSignal,
  onLoad: () => void,
  onError: () => void
): void {
  if (window.WxLogin) {
    onLoad()
    return
  }
  const script =
    document.querySelector<HTMLScriptElement>(`script[src="${sdkURL}"]`) ??
    document.createElement('script')
  if (!script.isConnected) {
    script.src = sdkURL
    script.async = true
    document.head.appendChild(script)
  }
  const cleanup = () => {
    clearTimeout(timeout)
    script.removeEventListener('load', loaded)
    script.removeEventListener('error', failed)
    signal.removeEventListener('abort', cleanup)
  }
  const loaded = () => {
    cleanup()
    if (window.WxLogin) onLoad()
    else failed()
  }
  const failed = () => {
    cleanup()
    script.remove()
    onError()
  }
  const timeout = window.setTimeout(failed, 15_000)
  script.addEventListener('load', loaded)
  script.addEventListener('error', failed)
  signal.addEventListener('abort', cleanup, { once: true })
  if (signal.aborted) cleanup()
}

type WechatOpenLoginProps = {
  capability: WeChatOpenCapability
  consentGranted: boolean
  redirectTo?: string
  registrationEnabled?: boolean
}

export function WechatOpenLogin(props: WechatOpenLoginProps) {
  const { t, i18n } = useTranslation()
  const { resolvedTheme } = useTheme()
  const containerID = `wechat-open-${useId().replaceAll(':', '')}`
  const container = useRef<HTMLDivElement>(null)
  const operation = useRef<AbortController | null>(null)
  const currentAttempt = useRef<WeChatOpenAttempt | null>(null)
  const [attempt, setAttempt] = useState<WeChatOpenAttempt | null>(null)
  const [phase, setPhase] = useState<'idle' | 'loading' | 'ready' | 'error'>(
    'idle'
  )
  const [errorMessage, setErrorMessage] = useState('')
  const lang = (i18n.resolvedLanguage || i18n.language || 'en').startsWith('zh')
    ? 'cn'
    : 'en'

  const cancel = useCallback(() => {
    operation.current?.abort()
    operation.current = null
    if (currentAttempt.current) {
      invalidateWeChatOpenAttempt(currentAttempt.current.state)
    }
    currentAttempt.current = null
    setAttempt(null)
    setPhase('idle')
    setErrorMessage('')
  }, [])

  useEffect(() => {
    cancel()
    return () => {
      operation.current?.abort()
      if (currentAttempt.current) {
        invalidateWeChatOpenAttempt(currentAttempt.current.state)
      }
      currentAttempt.current = null
    }
  }, [
    cancel,
    props.consentGranted,
    props.capability.enabled,
    props.capability.app_id,
    props.capability.redirect_uri,
    props.redirectTo,
  ])

  useEffect(
    () =>
      useAuthStore.subscribe((next, previous) => {
        if (
          next.auth.session?.sid !== previous.auth.session?.sid ||
          next.auth.user?.id !== previous.auth.user?.id
        ) {
          cancel()
        }
      }),
    [cancel]
  )

  const start = async () => {
    if (!props.consentGranted || !props.capability.enabled) return
    cancel()
    const controller = new AbortController()
    operation.current = controller
    setPhase('loading')
    try {
      const next = await createWeChatOpenLoginAttempt(
        props.redirectTo,
        controller.signal
      )
      if (controller.signal.aborted) {
        invalidateWeChatOpenAttempt(next.state)
        return
      }
      currentAttempt.current = next
      setAttempt(next)
    } catch (error) {
      if (controller.signal.aborted) return
      setErrorMessage(AuthOperationError.from(error).message)
      setPhase('error')
    }
  }

  useEffect(() => {
    if (!attempt) return
    const controller = new AbortController()
    const node = container.current
    let loginOptions: WxLoginOptions | undefined
    const isCurrent = () =>
      !controller.signal.aborted &&
      currentAttempt.current?.state === attempt.state
    const fail = (message: string) => {
      if (!isCurrent()) return
      invalidateWeChatOpenAttempt(attempt.state)
      currentAttempt.current = null
      setAttempt(null)
      setErrorMessage(message)
      setPhase('error')
      node?.replaceChildren()
    }
    const expiry = window.setTimeout(
      () => fail('WeChat login flow expired. Please start again.'),
      Math.max(0, attempt.expiresAt - Date.now())
    )
    const readyTimeout = window.setTimeout(
      () =>
        fail(
          'Unable to load WeChat login. Please try again or use account login.'
        ),
      20_000
    )
    setPhase('loading')
    loadWxLogin(
      controller.signal,
      () => {
        if (!isCurrent() || !window.WxLogin) return
        try {
          loginOptions = {
            id: containerID,
            appid: props.capability.app_id,
            scope: 'snsapi_login',
            redirect_uri: encodeURIComponent(props.capability.redirect_uri),
            state: attempt.state,
            self_redirect: false,
            style: resolvedTheme === 'dark' ? 'white' : 'black',
            lang,
            onReady: (ready) => {
              if (!isCurrent()) return
              clearTimeout(readyTimeout)
              if (ready) setPhase('ready')
              else {
                fail(
                  'Unable to load WeChat login. Please try again or use account login.'
                )
              }
            },
            onQRcodeReady: () => {
              clearTimeout(readyTimeout)
              if (isCurrent()) setPhase('ready')
            },
          }
          new window.WxLogin(loginOptions)
          const iframe = node?.querySelector('iframe')
          if (iframe) iframe.title = t('WeChat login / registration')
        } catch {
          fail(
            'Unable to load WeChat login. Please try again or use account login.'
          )
        }
      },
      () =>
        fail(
          'Unable to load WeChat login. Please try again or use account login.'
        )
    )
    return () => {
      controller.abort()
      // 官方 SDK 将清理函数写回参数对象，取消或替换组件时须移除窗口监听。
      loginOptions?.onCleanup?.()
      clearTimeout(expiry)
      clearTimeout(readyTimeout)
      node?.replaceChildren()
    }
  }, [
    attempt,
    containerID,
    props.capability.app_id,
    props.capability.redirect_uri,
    resolvedTheme,
    lang,
    t,
  ])

  if (!props.capability.enabled) return null

  return (
    <section
      aria-label={t('WeChat login / registration')}
      className='min-w-0 space-y-4 text-center'
    >
      <div>
        <IconWeChat aria-hidden='true' className='mx-auto mb-3 size-7' />
        <h3 className='text-lg font-medium'>
          {t('WeChat login / registration')}
        </h3>
        <p className='text-muted-foreground mt-1 text-sm'>
          {props.registrationEnabled === false
            ? t('New user registration is currently unavailable.')
            : t('First-time users will automatically get an account')}
        </p>
      </div>
      {!props.consentGranted && (
        <p className='text-muted-foreground text-sm'>
          {t('Please agree to the legal terms first')}
        </p>
      )}
      {attempt && phase !== 'error' && (
        <div
          ref={container}
          id={containerID}
          className='mx-auto min-h-[220px] min-w-[220px] overflow-hidden [&_iframe]:max-w-full'
        />
      )}
      {phase === 'loading' && (
        <div role='status'>
          <LoadingState inline size='sm' message={t('Loading WeChat login…')} />
        </div>
      )}
      {phase === 'ready' && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t('Confirm in WeChat to continue')}
        </p>
      )}
      {phase === 'error' && (
        <div role='alert'>
          <ErrorState
            title={t(errorMessage)}
            className='min-h-0 border-0 p-0'
          />
        </div>
      )}
      <div className='flex flex-wrap justify-center gap-2'>
        <Button
          type='button'
          variant='outline'
          disabled={!props.consentGranted || phase === 'loading'}
          onClick={() => void start()}
        >
          {phase === 'idle'
            ? t('Start WeChat login')
            : t('Restart WeChat login')}
        </Button>
        {phase !== 'idle' && (
          <Button type='button' variant='ghost' onClick={cancel}>
            {t('Cancel')}
          </Button>
        )}
      </div>
    </section>
  )
}
