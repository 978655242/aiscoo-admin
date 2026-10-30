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
import {
  onlineManager,
  QueryClientProvider,
  type QueryClient,
} from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, AxiosHeaders, type AxiosAdapter } from 'axios'
import i18next from 'i18next'
import { StrictMode } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { consumeOAuthLoginRedirect } from '@/features/auth/lib/oauth-callback-mode'
import { SignIn } from '@/features/auth/sign-in'
import { SignUp } from '@/features/auth/sign-up'
import type { SystemStatus } from '@/features/auth/types'
import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zhCN from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { applyAuthBundle } from '@/lib/auth-session'
import { createAppQueryClient } from '@/lib/query-client'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { XCodeAuthorize } from '../authorize'

const nativeStatus: Partial<SystemStatus> = {
  register_enabled: true,
  password_login_enabled: true,
  password_login_encryption_enabled: false,
  github_oauth: true,
  github_client_id: 'native-github',
  wechat_login: true,
}
vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ status: nativeStatus }),
}))
const returnTarget = '/xcode/authorize?flow_token=validated-flow'
const bundle: AuthBundle = {
  access_token: 'native-browser-access',
  token_type: 'Bearer',
  access_expires_at: 9999999999,
  user: {
    id: 42,
    username: 'native-user',
    display_name: 'Native User',
    role: 1,
  },
  session: {
    sid: 'native-browser-session',
    current: true,
    login_method: 'password',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: 9999999999,
  },
}
const otherBundle: AuthBundle = {
  ...bundle,
  access_token: 'other-browser-access',
  user: {
    ...bundle.user,
    id: 43,
    username: 'other-user',
    display_name: 'Other User',
  },
  session: { ...bundle.session, sid: 'other-browser-session' },
}
let client: QueryClient
const openClient = vi.fn()
const originalAdapter = api.defaults.adapter

async function renderAuthorization(
  initialEntry = returnTarget,
  authenticated = false,
  strict = false
) {
  if (authenticated) useAuthStore.getState().auth.setBundle(bundle)
  const root = createRootRoute({ component: Outlet })
  const auth = createRoute({
    getParentRoute: () => root,
    id: '(auth)',
    component: Outlet,
  })
  const signIn = createRoute({
    getParentRoute: () => auth,
    path: 'sign-in',
    component: SignIn,
    validateSearch: (search: Record<string, unknown>) => ({
      redirect:
        typeof search.redirect === 'string' ? search.redirect : undefined,
    }),
  })
  const signUp = createRoute({
    getParentRoute: () => auth,
    path: 'sign-up',
    component: SignUp,
    validateSearch: (search: Record<string, unknown>) => ({
      redirect:
        typeof search.redirect === 'string' ? search.redirect : undefined,
    }),
  })
  const authorize = createRoute({
    getParentRoute: () => root,
    path: '/xcode/authorize',
    validateSearch: (search: Record<string, unknown>) => ({
      flow_token:
        typeof search.flow_token === 'string' ? search.flow_token : '',
    }),
    component: () => (
      <XCodeAuthorize flowToken={authorize.useSearch().flow_token} />
    ),
  })
  const dashboard = createRoute({
    getParentRoute: () => root,
    path: '/dashboard',
    component: () => <div>Native dashboard</div>,
  })
  const router = createRouter({
    routeTree: root.addChildren([
      auth.addChildren([signIn, signUp]),
      authorize,
      dashboard,
    ]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  client = createAppQueryClient()
  const tree = (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  render(strict ? <StrictMode>{tree}</StrictMode> : tree)
  return router
}

function mockLiveRequest(
  expiresAt = Date.now() + 600000,
  clientID = 'xcode-desktop'
) {
  return vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        schema_version: 1,
        server_time: Date.now(),
        client_id: clientID,
        expires_at: expiresAt,
      },
    },
  })
}

function holdAuthRefresh(responseBundle: AuthBundle) {
  let markStarted!: (request: { url: string; sid?: string }) => void
  const started = new Promise<{ url: string; sid?: string }>((resolve) => {
    markStarted = resolve
  })
  let finish!: () => void
  const response = new Promise<void>((resolve) => {
    finish = resolve
  })
  vi.stubGlobal(
    'XMLHttpRequest',
    class {
      status = 200
      statusText = 'OK'
      readyState = 4
      responseText = JSON.stringify({ success: true, data: responseBundle })
      onloadend: (() => void) | null = null
      url = ''
      headers: Record<string, string> = {}
      open(_method: string, url: string) {
        this.url = url
      }
      setRequestHeader(name: string, value: string) {
        this.headers[name] = value
      }
      getAllResponseHeaders() {
        return 'content-type: application/json'
      }
      abort() {}
      send() {
        markStarted({ url: this.url, sid: this.headers['X-Auth-Session'] })
        void response.then(() => this.onloadend?.())
      }
    }
  )
  return { started, finish }
}

beforeEach(() => {
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useSystemConfigStore.getState().setLoading(false)
  useAuthStore.getState().auth.reset('complete')
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  window.sessionStorage.clear()
  openClient.mockReset()
  vi.stubGlobal('location', { ...window.location, assign: openClient })
})
afterEach(() => {
  cleanup()
  client?.clear()
  onlineManager.setOnline(true)
  api.defaults.adapter = originalAdapter
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset('idle')
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

it.each([
  [returnTarget, true],
  [`/sign-in?redirect=${encodeURIComponent(returnTarget)}`, false],
  [`/sign-up?redirect=${encodeURIComponent(returnTarget)}`, false],
])('rejects framed authentication surface %s', async (path, authenticated) => {
  vi.stubGlobal('top', {})
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(path, authenticated)
  expect(await screen.findByRole('alert')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Authorize and open aiscoo' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Sign in' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Create account' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

it.each(
  (
    [
      ['en', en],
      ['zhCN', zhCN],
      ['zhTW', zhTW],
      ['fr', fr],
      ['ja', ja],
      ['ru', ru],
      ['vi', viLocale],
    ] as const
  ).flatMap(([language, resources]) =>
    [
      ['xcode-desktop', 'aiscoo client (desktop)'],
      ['xwork-cli', 'aiscoo client (CLI)'],
    ].map(
      ([clientID, label]) => [language, clientID, resources, label] as const
    )
  )
)(
  '%s shows the branded %s client and account-switch dialog without legacy names',
  async (language, clientID, resources, label) => {
    i18next.addResourceBundle(language, 'translation', resources.translation)
    await i18next.changeLanguage(language)
    try {
      mockLiveRequest(Date.now() + 600000, clientID)
      await renderAuthorization(returnTarget, true)
      await screen.findByRole('heading', {
        name: i18next.t('Authorize aiscoo'),
      })
      expect(screen.getByRole('img', { name: 'aiscoo' })).toBeVisible()
      expect(
        screen.getByText(
          i18next.t('Use your assigned aiscoo plan to call models')
        )
      ).toBeVisible()
      for (const description of [
        'aiscoo cannot manage payments, API keys or account security.',
        'Authorization does not automatically grant a plan.',
        'aiscoo connects using a separate device session. Your browser stays signed in.',
        'The device session follows the native aiscoo session lifetime (30 days by default). You can revoke the aiscoo device in Login Sessions.',
      ]) {
        expect(screen.getByText(i18next.t(description))).toBeVisible()
      }
      const applicationLabel = screen.getByText(i18next.t(label))
      expect(applicationLabel).toBeVisible()
      // 品牌断言只约束产品文案，不能重写用户展示名、站点自定义名或来源署名。
      expect(applicationLabel).not.toHaveTextContent(
        /new[\s-]*api|x[\s-]*code|xwork/i
      )
      expect(
        screen.getByRole('region', { name: i18next.t('Permissions') })
      ).not.toHaveTextContent(/new[\s-]*api|x[\s-]*code|xwork/i)
      await userEvent.setup().click(
        screen.getByRole('button', {
          name: i18next.t('Use another account'),
        })
      )
      const dialog = await screen.findByRole('alertdialog')
      expect(dialog).toBeVisible()
      expect(
        within(dialog).getByRole('heading', {
          name: i18next.t('Switch login account?'),
        })
      ).not.toHaveTextContent(/new[\s-]*api|x[\s-]*code|xwork/i)
    } finally {
      cleanup()
      await i18next.changeLanguage('en')
      i18next.removeResourceBundle(language, 'translation')
    }
  }
)

it('preserves account names containing XCode while branding the authorized application', async () => {
  mockLiveRequest()
  await renderAuthorization(returnTarget, true)
  await screen.findByRole('heading', { name: 'Authorize aiscoo' })
  act(() => {
    useAuthStore.getState().auth.setBundle({
      ...bundle,
      user: {
        ...bundle.user,
        display_name: 'XCode contributor',
        username: 'new-api-user',
      },
    })
  })
  const account = screen.getByRole('region', { name: 'Current account' })
  expect(within(account).getByText('XCode contributor')).toBeVisible()
  expect(within(account).getByText('new-api-user')).toBeVisible()
  expect(screen.getByText('aiscoo client (desktop)')).toBeVisible()
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Use another account' }))
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('new-api-user')
})

it('shows the request after a StrictMode remount cancels the first deduplicated fetch', async () => {
  // Real api.get (with its in-flight GET sharing) over a signal-aware adapter.
  api.defaults.adapter = (async (config) => {
    await Promise.resolve()
    if (config.signal?.aborted) {
      throw new AxiosError('canceled', AxiosError.ERR_CANCELED, config)
    }
    return {
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          client_id: 'xcode-desktop',
          expires_at: Date.now() + 600000,
        },
      },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }) as AxiosAdapter
  await renderAuthorization(returnTarget, true, true)
  expect(
    await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
  ).toBeEnabled()
  expect(
    screen.queryByText('Authorization request unavailable')
  ).not.toBeInTheDocument()
})

it('validates the request before native sign-in and preserves it through sign-up, registration and native sign-in', async () => {
  const get = mockLiveRequest()
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/user/register') return { data: { success: true } }
    if (String(url).startsWith('/api/user/login')) {
      return { data: { success: true, data: bundle } }
    }
    throw new Error('Unexpected request')
  })
  const router = await renderAuthorization()
  const user = userEvent.setup()
  await screen.findByRole('heading', { name: 'Sign in' })
  expect(get).toHaveBeenCalledWith(
    '/api/xcode/v1/auth/authorize/request',
    expect.objectContaining({ params: { flow_token: 'validated-flow' } })
  )
  expect(router.state.location.search).toEqual({ redirect: returnTarget })
  await user.click(screen.getByRole('link', { name: 'Sign up' }))
  await screen.findByRole('heading', { name: 'Create an account' })
  expect(router.state.location.search).toEqual({ redirect: returnTarget })
  await user.click(screen.getByRole('link', { name: 'Sign in' }))
  await screen.findByRole('heading', { name: 'Sign in' })
  expect(router.state.location.search).toEqual({ redirect: returnTarget })
  await user.click(screen.getByRole('link', { name: 'Sign up' }))
  await user.type(await screen.findByLabelText('Username'), 'native-user')
  await user.type(
    screen.getByLabelText('Password', { exact: true }),
    'SafePassword123!'
  )
  await user.type(
    screen.getByLabelText('Confirm password', { exact: true }),
    'SafePassword123!'
  )
  await user.click(screen.getByRole('button', { name: 'Create account' }))
  await screen.findByRole('heading', { name: 'Sign in' })
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(router.state.location.search).toEqual({ redirect: returnTarget })
  await user.type(screen.getByLabelText('Username or Email'), 'native-user')
  await user.type(
    screen.getByLabelText('Password', { exact: true }),
    'SafePassword123!'
  )
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await screen.findByRole('heading', { name: 'Authorize aiscoo' })
  expect(router.state.location.href).toBe(returnTarget)
  expect(screen.getByText('native-user')).toBeVisible()
  expect(screen.getByText('aiscoo client (desktop)')).toBeVisible()
  expect(useAuthStore.getState().auth.session?.sid).toBe(
    'native-browser-session'
  )
  expect(
    post.mock.calls.some(([url]) => String(url).includes('authorize/complete'))
  ).toBe(false)
})

it.each([
  'https://attacker.example/steal',
  '//attacker.example/steal',
  'zcode://oauth/callback',
])(
  'drops external native auth return target %s when switching sign-in and sign-up',
  async (target) => {
    const router = await renderAuthorization(
      `/sign-in?redirect=${encodeURIComponent(target)}`
    )
    const user = userEvent.setup()
    await user.click(await screen.findByRole('link', { name: 'Sign up' }))
    expect(router.state.location.search.redirect).toBeUndefined()
    await user.click(await screen.findByRole('link', { name: 'Sign in' }))
    expect(router.state.location.search.redirect).toBeUndefined()
  }
)

it('preserves the sanitized return target when native sign-up starts OAuth', async () => {
  vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/user/auth/logout') return { data: { success: true } }
    if (url === '/api/oauth/state') {
      return {
        data: { success: true, data: { flow_token: 'native-oauth-state' } },
      }
    }
    throw new Error('Unexpected request')
  })
  const open = vi.spyOn(window, 'open').mockImplementation(() => null)
  await renderAuthorization(
    `/sign-up?redirect=${encodeURIComponent(returnTarget)}`
  )
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: /Continue with GitHub$/ }))
  await waitFor(() => expect(open).toHaveBeenCalled())
  expect(consumeOAuthLoginRedirect('native-oauth-state')).toBe(returnTarget)
})

it('returns a native sign-up WeChat login to the authorization request', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/oauth/wechat') {
      return { data: { success: true, data: bundle } }
    }
    return {
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          client_id: 'xcode-desktop',
          expires_at: Date.now() + 600000,
        },
      },
    }
  })
  const router = await renderAuthorization(
    `/sign-up?redirect=${encodeURIComponent(returnTarget)}`
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: /Continue with WeChat$/ })
  )
  await user.type(await screen.findByLabelText('Verification code'), '123456')
  await user.click(
    within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' })
  )
  await screen.findByRole('heading', { name: 'Authorize aiscoo' })
  expect(router.state.location.href).toBe(returnTarget)
  expect(useAuthStore.getState().auth.session?.sid).toBe(
    'native-browser-session'
  )
})

it.each(['expired', 'invalid'])(
  'shows a terminal error for an %s request without redirecting to login',
  async (failure) => {
    const get =
      failure === 'expired'
        ? mockLiveRequest(Date.now() - 1)
        : vi.spyOn(api, 'get').mockResolvedValue({
            data: {
              success: false,
              code: 'XCODE_AUTH_REQUEST_INVALID',
              message: 'Invalid authorization request',
            },
          })
    const router = await renderAuthorization()
    expect(
      await screen.findByText('Authorization request unavailable')
    ).toBeVisible()
    expect(router.state.location.pathname).toBe('/xcode/authorize')
    expect(
      screen.queryByRole('heading', { name: 'Sign in' })
    ).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledTimes(1)
  }
)

it.each(['network', 'unauthorized'])(
  'spends a one-use authorization only once after %s failure without replay',
  async (failure) => {
    mockLiveRequest()
    const refreshRequest = vi
      .spyOn(XMLHttpRequest.prototype, 'send')
      .mockImplementation(() => {
        throw new Error('Unexpected authentication replay')
      })
    const adapter = vi.fn<AxiosAdapter>(async (config) => {
      throw new AxiosError(
        'Request failed',
        failure === 'network' ? 'ERR_NETWORK' : 'ERR_BAD_REQUEST',
        config,
        undefined,
        failure === 'unauthorized'
          ? {
              status: 401,
              statusText: 'Unauthorized',
              config,
              headers: {},
              data: { success: false },
            }
          : undefined
      )
    })
    api.defaults.adapter = adapter
    await renderAuthorization(returnTarget, true)
    const user = userEvent.setup()
    const consent = await screen.findByRole('button', {
      name: 'Authorize and open aiscoo',
    })
    expect(
      screen.getByText(
        'aiscoo cannot manage payments, API keys or account security.'
      )
    ).toBeVisible()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(adapter).not.toHaveBeenCalled()
    await user.dblClick(consent)
    expect(
      await screen.findByText('Authorization could not be completed')
    ).toBeVisible()
    expect(adapter).toHaveBeenCalledTimes(1)
    const sent = adapter.mock.calls[0][0]
    expect(sent.url).toBe('/api/xcode/v1/auth/authorize/complete')
    expect(sent.method).toBe('post')
    expect(JSON.parse(sent.data)).toEqual({ flow_token: 'validated-flow' })
    expect(sent.headers.Authorization).toBe('Bearer native-browser-access')
    expect(sent.singleUseAuthorization).toBe(true)
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
      window.dispatchEvent(new Event('online'))
    })
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(refreshRequest).not.toHaveBeenCalled()
    expect(openClient).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: /Retry|Authorize/ })
    ).not.toBeInTheDocument()
    expect(useAuthStore.getState().auth.session?.sid).toBe(
      'native-browser-session'
    )
  }
)

it.each([
  'https://attacker.example/oauth/callback?provider=new-api&code=c&state=s',
  'zcode://oauth/other?provider=new-api&code=c&state=s',
  'zcode://oauth:42/callback?provider=new-api&code=c&state=s',
  'zcode://user@oauth/callback?provider=new-api&code=c&state=s',
  'zcode://oauth/callback?provider=other&code=c&state=s',
  'zcode://oauth/callback?provider=new-api&code=c&state=s&access_token=secret',
  'zcode://oauth/callback?provider=new-api&code=c&state=s&state=duplicate',
  'zcode://oauth/callback?provider=new-api&code=&state=s',
  'zcode://oauth/callback?provider=new-api&code=c&state=',
  'zcode://oauth/callback?provider=new-api&code=c&state=s#fragment',
  'http://127.0.0.1:49152/oauth/callback?provider=new-api&code=c&state=s',
  ...['aiscoo', 'aiscoo-dev', 'aiscoo-preview'].flatMap((scheme) => [
    `${scheme.toUpperCase()}://oauth/callback?provider=new-api&code=c&state=s`,
    `${scheme}://OAUTH/callback?provider=new-api&code=c&state=s`,
    `${scheme}://oauth/callback/?provider=new-api&code=c&state=s`,
    `${scheme}://oauth/%63allback?provider=new-api&code=c&state=s`,
    `${scheme}://oauth/callback.evil?provider=new-api&code=c&state=s`,
    `${scheme}://oauth.evil/callback?provider=new-api&code=c&state=s`,
    `${scheme}://user@oauth/callback?provider=new-api&code=c&state=s`,
    `${scheme}://oauth:443/callback?provider=new-api&code=c&state=s`,
    `${scheme}://oauth/callback?provider=new-api&code=c&state=s#fragment`,
    `${scheme}://oauth/callback?provider=other&code=c&state=s`,
    `${scheme}://oauth/callback?provider=new-api&code=c&state=s&refresh_token=secret`,
    `${scheme}://oauth/callback?provider=new-api&code=c&state=s&code=duplicate`,
    `${scheme}://oauth/callback?provider=new-api&code=&state=s`,
    `${scheme}://oauth/callback?provider=new-api&code=c&state=`,
  ]),
])(
  'rejects an unsafe completion callback %s without exposing credentials or offering replay',
  async (redirectURL) => {
    mockLiveRequest()
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          redirect_url: redirectURL,
        },
      },
    })
    await renderAuthorization(returnTarget, true)
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
    )
    expect(
      await screen.findByText('Authorization could not be completed')
    ).toBeVisible()
    expect(screen.queryByText(redirectURL)).not.toBeInTheDocument()
    expect(openClient).not.toHaveBeenCalled()
    expect(post).toHaveBeenCalledTimes(1)
    expect(
      screen.queryByRole('button', { name: /Retry|Authorize/ })
    ).not.toBeInTheDocument()
  }
)

it('opens the registered CLI loopback callback after native authorization', async () => {
  mockLiveRequest(Date.now() + 600000, 'xwork-cli')
  const callback =
    'http://127.0.0.1:49152/oauth/callback?provider=new-api&code=one-use-code&state=host-state'
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        schema_version: 1,
        server_time: Date.now(),
        redirect_url: callback,
      },
    },
  })
  await renderAuthorization(returnTarget, true)
  await userEvent
    .setup()
    .click(
      await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
    )
  await screen.findByText('Opening aiscoo...')
  expect(openClient).toHaveBeenCalledExactlyOnceWith(callback)
})

it.each(['aiscoo', 'aiscoo-dev', 'aiscoo-preview', 'zcode'])(
  'rejects the desktop %s callback for a CLI authorization',
  async (scheme) => {
    mockLiveRequest(Date.now() + 600000, 'xwork-cli')
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          redirect_url: `${scheme}://oauth/callback?provider=new-api&code=c&state=s`,
        },
      },
    })
    await renderAuthorization(returnTarget, true)
    await userEvent
      .setup()
      .click(
        await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
      )
    expect(
      await screen.findByText('Authorization could not be completed')
    ).toBeVisible()
    expect(openClient).not.toHaveBeenCalled()
  }
)

it('identifies the CLI as the authorized client instead of the desktop app', async () => {
  mockLiveRequest(Date.now() + 600000, 'xwork-cli')
  await renderAuthorization(returnTarget, true)
  expect(
    await screen.findByText(
      'Connect aiscoo CLI on this computer with your aiscoo account'
    )
  ).toBeVisible()
  expect(
    screen.queryByText('Connect the desktop app with your aiscoo account')
  ).not.toBeInTheDocument()
  expect(screen.getByText('aiscoo client (CLI)')).toBeVisible()
  expect(screen.queryByText('aiscoo client (desktop)')).not.toBeInTheDocument()
})

it.each([
  [
    'xcode-desktop',
    'http://127.0.0.1:49152/oauth/callback?provider=new-api&code=c&state=s',
  ],
  ['xwork-cli', 'zcode://oauth/callback?provider=new-api&code=c&state=s'],
])(
  'rejects a callback registered to another client for %s',
  async (clientID, redirectURL) => {
    mockLiveRequest(Date.now() + 600000, clientID)
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          redirect_url: redirectURL,
        },
      },
    })
    await renderAuthorization(returnTarget, true)
    await userEvent
      .setup()
      .click(
        await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
      )
    expect(
      await screen.findByText('Authorization could not be completed')
    ).toBeVisible()
    expect(openClient).not.toHaveBeenCalled()
  }
)

it.each([
  'aiscoo://oauth/callback',
  'aiscoo-dev://oauth/callback',
  'aiscoo-preview://oauth/callback',
  'zcode://oauth/callback',
])(
  'opens only the registered desktop callback %s once without storing credentials',
  async (redirectURI) => {
    mockLiveRequest()
    const callback = `${redirectURI}?provider=new-api&code=one-use-code&state=host-state`
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          redirect_url: callback,
        },
      },
    })
    await renderAuthorization(returnTarget, true)
    const localBefore = { ...window.localStorage }
    const sessionBefore = { ...window.sessionStorage }
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
    )
    await screen.findByText('Opening aiscoo...')
    expect(openClient).toHaveBeenCalledExactlyOnceWith(callback)
    expect(post).toHaveBeenCalledTimes(1)
    expect({ ...window.localStorage }).toEqual(localBefore)
    expect({ ...window.sessionStorage }).toEqual(sessionBefore)
    expect(useAuthStore.getState().auth.session?.sid).toBe(
      'native-browser-session'
    )
    expect(useAuthStore.getState().auth.accessToken).toBe(
      'native-browser-access'
    )
    expect(
      screen.queryByText(/one-use-code|host-state/)
    ).not.toBeInTheDocument()
  }
)

it('closes a switch confirmation synchronously across A→B→A', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(returnTarget, true)
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Use another account' }))
  await screen.findByRole('alertdialog')
  act(() => {
    useAuthStore.getState().auth.setBundle(otherBundle)
    useAuthStore.getState().auth.setBundle(bundle)
  })
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(post).not.toHaveBeenCalled()
  expect(openClient).not.toHaveBeenCalled()
})

async function confirmAccountSwitch() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Use another account' })
  )
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Sign out and switch',
    })
  )
}

it('returns from the switch dialog without changing the browser account', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(returnTarget, true)
  const user = userEvent.setup()
  const trigger = await screen.findByRole('button', {
    name: 'Use another account',
  })
  await user.click(trigger)
  expect(
    await screen.findByText(
      'Other open aiscoo dashboard pages will also be affected by this account switch.'
    )
  ).toBeVisible()
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Back',
    })
  )
  expect(post).not.toHaveBeenCalled()
  expect(useAuthStore.getState().auth.user?.id).toBe(42)
  await waitFor(() => expect(trigger).toHaveFocus())
})

it('clears real query caches and carries the entire flow through native sign-in as B without automatic consent', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/user/auth/logout') {
      return {
        data: { success: true, data: { revoked_sid: bundle.session.sid } },
      }
    }
    if (String(url).startsWith('/api/user/login')) {
      return { data: { success: true, data: otherBundle } }
    }
    throw new Error('Unexpected request')
  })
  const target =
    '/xcode/authorize?flow_token=flow%2Bwith%2Freserved%3Dcharacters'
  const router = await renderAuthorization(target, true)
  client.setQueryData(['private-account-data'], 'A only')
  await confirmAccountSwitch()
  await screen.findByRole('heading', { name: 'Sign in' })
  expect(router.state.location.search.redirect).toBe(target)
  expect(client.getQueryData(['private-account-data'])).toBeUndefined()
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(post).toHaveBeenCalledWith(
    '/api/user/auth/logout',
    undefined,
    expect.objectContaining({
      headers: { 'X-Auth-Session': bundle.session.sid },
    })
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Username or Email'), 'other-user')
  await user.type(
    screen.getByLabelText('Password', { exact: true }),
    'SafePassword123!'
  )
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await screen.findByRole('heading', { name: 'Authorize aiscoo' })
  expect(screen.getByText('other-user')).toBeVisible()
  expect(router.state.location.href).toBe(target)
  expect(
    post.mock.calls.filter(([url]) =>
      String(url).includes('authorize/complete')
    )
  ).toHaveLength(0)
})

it.each([
  'business',
  'network',
  'server',
  'mismatch',
  'anonymous',
  'wrong-sid',
])('does not clear or navigate after %s logout failure', async (failure) => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post').mockImplementation(async () => {
    if (failure === 'business') {
      return { data: { success: false, message: 'Denied' } }
    }
    if (failure === 'anonymous') return { data: { success: true } }
    if (failure === 'wrong-sid') {
      return {
        data: { success: true, data: { revoked_sid: otherBundle.session.sid } },
      }
    }
    if (failure === 'network') throw new Error('Offline')
    throw new AxiosError('Failed', undefined, undefined, undefined, {
      status: failure === 'mismatch' ? 409 : 503,
      data: { success: false, code: 'AUTH_SESSION_MISMATCH' },
      headers: {},
      config: { headers: new AxiosHeaders() },
      statusText: 'Failed',
    })
  })
  const router = await renderAuthorization(returnTarget, true)
  client.setQueryData(['private-account-data'], 'A only')
  await confirmAccountSwitch()
  if (failure === 'mismatch') {
    expect(
      await screen.findByText('Account identity is temporarily unavailable')
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Use another account' })
    ).not.toBeInTheDocument()
  } else {
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not sign out.'
    )
  }
  expect(useAuthStore.getState().auth.session?.sid).toBe(bundle.session.sid)
  expect(client.getQueryData(['private-account-data'])).toBe('A only')
  expect(router.state.location.href).toBe(returnTarget)
  expect(post).toHaveBeenCalledTimes(1)
})

it.each([
  ['new-flow', true],
  ['new-flow', false],
  ['new-owner', true],
  ['new-owner', false],
  ['A→B→A', true],
  ['A→B→A', false],
  ['unmount', true],
  ['unmount', false],
])('ignores a late logout after %s (success=%s)', async (change, success) => {
  mockLiveRequest()
  let resolve!: (response: {
    data: { success: boolean; data?: { revoked_sid: string } }
  }) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<{
    data: { success: boolean; data?: { revoked_sid: string } }
  }>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  const post = vi.spyOn(api, 'post').mockReturnValue(promise)
  const router = await renderAuthorization(returnTarget, true)
  await confirmAccountSwitch()
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  if (change === 'A→B→A' || change === 'new-owner') {
    act(() => {
      useAuthStore.getState().auth.setBundle(otherBundle)
      if (change === 'A→B→A') useAuthStore.getState().auth.setBundle(bundle)
    })
  } else {
    await act(async () =>
      router.navigate({
        to: change === 'unmount' ? '/dashboard' : '/xcode/authorize',
        search: change === 'unmount' ? {} : { flow_token: 'new-flow' },
      })
    )
  }
  await act(async () => {
    if (success) {
      resolve({
        data: { success: true, data: { revoked_sid: bundle.session.sid } },
      })
    } else reject(new Error('Late offline failure'))
  })
  expect(useAuthStore.getState().auth.session?.sid).toBe(
    change === 'new-owner' ? otherBundle.session.sid : bundle.session.sid
  )
  expect(router.state.location.pathname).not.toBe('/sign-in')
  expect(openClient).not.toHaveBeenCalled()
  expect(
    screen.queryByText(
      'Could not sign out. Your account has not been cleared. Try again.'
    )
  ).not.toBeInTheDocument()
})

it('keeps authorization cancelled as a terminal state without logout, completion or deep link', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(returnTarget, true)
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Cancel' }))
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Authorization cancelled.'
  )
  act(() => {
    useAuthStore.getState().auth.setBundle(otherBundle)
    useAuthStore.getState().auth.setBundle(bundle)
  })
  expect(
    screen.queryByRole('button', { name: 'Authorize and open aiscoo' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
  expect(openClient).not.toHaveBeenCalled()
  expect(useAuthStore.getState().auth.user?.id).toBe(42)
})

it('does not restore A when sign-in is interrupted after a successful logout', async () => {
  mockLiveRequest()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { revoked_sid: bundle.session.sid } },
  })
  const router = await renderAuthorization(returnTarget, true)
  await confirmAccountSwitch()
  await screen.findByRole('heading', { name: 'Sign in' })
  await act(async () => router.navigate({ to: '/dashboard' }))
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(openClient).not.toHaveBeenCalled()
})

it('rejects the original deadline after time spent signing in without renewing the flow', async () => {
  const now = Date.now()
  let clock = now
  vi.spyOn(Date, 'now').mockImplementation(() => clock)
  const get = mockLiveRequest(now + 600000)
  get.mockImplementation(async () => ({
    data: {
      success: true,
      data: {
        schema_version: 1,
        server_time: clock,
        client_id: 'xcode-desktop',
        expires_at: now + 600000,
      },
    },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => ({
    data: {
      success: true,
      ...(String(url).startsWith('/api/user/login')
        ? { data: otherBundle }
        : { data: { revoked_sid: bundle.session.sid } }),
    },
  }))
  await renderAuthorization(returnTarget, true)
  await confirmAccountSwitch()
  await screen.findByRole('heading', { name: 'Sign in' })
  clock += 600001
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Username or Email'), 'other-user')
  await user.type(
    screen.getByLabelText('Password', { exact: true }),
    'SafePassword123!'
  )
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  expect(
    await screen.findByText('Authorization request unavailable')
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Authorize and open aiscoo' })
  ).not.toBeInTheDocument()
  expect(
    post.mock.calls.filter(([url]) =>
      String(url).includes('authorize/complete')
    )
  ).toHaveLength(0)
})

it.each(['complete', 'switch'])(
  'ends an offline-paused %s action when its deadline passes before reconnect',
  async (kind) => {
    let clock = Date.now()
    vi.spyOn(Date, 'now').mockImplementation(() => clock)
    mockLiveRequest(clock + 600000)
    const post = vi.spyOn(api, 'post')
    const router = await renderAuthorization(returnTarget, true)
    await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
    onlineManager.setOnline(false)
    if (kind === 'switch') {
      await confirmAccountSwitch()
    } else {
      await userEvent
        .setup()
        .click(
          screen.getByRole('button', { name: 'Authorize and open aiscoo' })
        )
    }
    await waitFor(() =>
      expect(
        client
          .getMutationCache()
          .getAll()
          .some((mutation) => mutation.state.isPaused)
      ).toBe(true)
    )
    expect(post).not.toHaveBeenCalled()
    clock += 600001
    await act(async () => onlineManager.setOnline(true))
    expect(
      await screen.findByText('Authorization request unavailable')
    ).toBeVisible()
    expect(screen.queryByText('Authorizing aiscoo...')).not.toBeInTheDocument()
    expect(screen.queryByText('Signing out...')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Authorize and open aiscoo' })
    ).not.toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
    expect(openClient).not.toHaveBeenCalled()
    expect(useAuthStore.getState().auth.session?.sid).toBe(bundle.session.sid)
    expect(router.state.location.href).toBe(returnTarget)
  }
)

it.each([
  ['A→B→A', true],
  ['A→B→A', false],
  ['new-owner', true],
  ['new-owner', false],
  ['new-flow', true],
  ['new-flow', false],
  ['unmount', true],
  ['unmount', false],
])(
  'ignores a late completion after %s (success=%s)',
  async (change, success) => {
    mockLiveRequest()
    type CompletionResponse = {
      data: {
        success: boolean
        data: {
          schema_version: number
          server_time: number
          redirect_url: string
        }
      }
    }
    let finish!: (response: CompletionResponse) => void
    let reject!: (reason?: unknown) => void
    const promise = new Promise<CompletionResponse>(
      (resolvePromise, rejectPromise) => {
        finish = resolvePromise
        reject = rejectPromise
      }
    )
    // XCODE: 故意保留迟到结果，验证 transport 未取消时页面仍拒绝旧动作。
    const post = vi.spyOn(api, 'post').mockReturnValue(promise)
    const router = await renderAuthorization(returnTarget, true)
    await userEvent
      .setup()
      .click(
        await screen.findByRole('button', { name: 'Authorize and open aiscoo' })
      )
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    if (change === 'A→B→A' || change === 'new-owner') {
      act(() => {
        useAuthStore.getState().auth.setBundle(otherBundle)
        if (change === 'A→B→A') useAuthStore.getState().auth.setBundle(bundle)
      })
    } else {
      await act(async () =>
        router.navigate({
          to: change === 'unmount' ? '/dashboard' : '/xcode/authorize',
          search: change === 'unmount' ? {} : { flow_token: 'new-flow' },
        })
      )
    }
    await act(async () => {
      if (success) {
        finish({
          data: {
            success: true,
            data: {
              schema_version: 1,
              server_time: Date.now(),
              redirect_url:
                'zcode://oauth/callback?provider=new-api&code=c&state=s',
            },
          },
        })
      } else {
        reject(new Error('Late completion failure'))
      }
    })
    expect(openClient).not.toHaveBeenCalled()
    expect(post).toHaveBeenCalledTimes(1)
    expect(useAuthStore.getState().auth.session?.sid).toBe(
      change === 'new-owner' ? otherBundle.session.sid : bundle.session.sid
    )
    expect(
      screen.queryByText('Authorization could not be completed')
    ).not.toBeInTheDocument()
    expect(router.state.location.pathname).not.toBe('/sign-in')
  }
)

it('checks the synchronous store before sending consent from a stale rendered owner', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(returnTarget, true)
  const consent = await screen.findByRole('button', {
    name: 'Authorize and open aiscoo',
  })
  act(() => {
    useAuthStore.getState().auth.setBundle(otherBundle)
    consent.click()
  })
  expect(post).not.toHaveBeenCalled()
  expect(await screen.findByText('other-user')).toBeVisible()
})

it.each([
  'new-owner',
  'A→B→A',
  'new-flow',
  'unmount',
  'refreshed-owner',
  'same-owner',
])(
  'gates actual adapter dispatch after auth preparation across %s',
  async (change) => {
    mockLiveRequest()
    const refresh = holdAuthRefresh({
      ...(change === 'refreshed-owner' ? otherBundle : bundle),
      access_token: 'refreshed-access',
    })
    const adapter = vi.fn<AxiosAdapter>(async (config) => ({
      status: 200,
      statusText: 'OK',
      config,
      headers: {},
      data: {
        success: true,
        data: {
          schema_version: 1,
          server_time: Date.now(),
          redirect_url:
            'zcode://oauth/callback?provider=new-api&code=c&state=s',
        },
      },
    }))
    api.defaults.adapter = adapter
    const router = await renderAuthorization(returnTarget, true)
    const consent = await screen.findByRole('button', {
      name: 'Authorize and open aiscoo',
    })
    act(() =>
      useAuthStore.getState().auth.setBundle({
        ...bundle,
        access_expires_at: Math.floor(Date.now() / 1000) + 10,
      })
    )
    await userEvent.setup().click(consent)
    try {
      expect(await refresh.started).toEqual({
        url: '/api/user/auth/refresh',
        sid: 'native-browser-session',
      })
      expect(adapter).not.toHaveBeenCalled()
      if (change === 'new-owner' || change === 'A→B→A') {
        act(() => {
          applyAuthBundle(otherBundle, false)
          if (change === 'A→B→A') applyAuthBundle(bundle, false)
        })
      } else if (change === 'new-flow' || change === 'unmount') {
        await act(async () =>
          router.navigate({
            to: change === 'unmount' ? '/dashboard' : '/xcode/authorize',
            search: change === 'unmount' ? {} : { flow_token: 'new-flow' },
          })
        )
      }
    } finally {
      await act(async () => refresh.finish())
    }
    await waitFor(() =>
      expect(
        client
          .getMutationCache()
          .getAll()
          .some((mutation) => mutation.state.status === 'pending')
      ).toBe(false)
    )
    if (change === 'same-owner') {
      expect(adapter).toHaveBeenCalledTimes(1)
      const sent = adapter.mock.calls[0][0]
      expect(sent.url).toBe('/api/xcode/v1/auth/authorize/complete')
      expect(sent.method).toBe('post')
      expect(JSON.parse(sent.data)).toEqual({ flow_token: 'validated-flow' })
      expect(sent.headers.Authorization).toBe('Bearer refreshed-access')
      expect(openClient).toHaveBeenCalledExactlyOnceWith(
        'zcode://oauth/callback?provider=new-api&code=c&state=s'
      )
    } else {
      expect(adapter).not.toHaveBeenCalled()
      expect(openClient).not.toHaveBeenCalled()
      expect(
        screen.queryByText('Authorization could not be completed')
      ).not.toBeInTheDocument()
    }
    expect(useAuthStore.getState().auth.session?.sid).toBe(
      change === 'new-owner' || change === 'refreshed-owner'
        ? 'other-browser-session'
        : 'native-browser-session'
    )
  }
)

it('uses the real username when the display name is empty', async () => {
  mockLiveRequest()
  await renderAuthorization(returnTarget, true)
  await screen.findByRole('heading', { name: 'Authorize aiscoo' })
  act(() =>
    useAuthStore.getState().auth.setBundle({
      ...bundle,
      user: { ...bundle.user, display_name: '   ' },
    })
  )
  expect(screen.getAllByText('native-user')).toHaveLength(2)
  expect(screen.queryByText(/administrator/i)).not.toBeInTheDocument()
})

it('shows a local ID-based avatar that survives renaming and updates with the current account', async () => {
  vi.spyOn(HTMLImageElement.prototype, 'complete', 'get').mockReturnValue(true)
  vi.spyOn(HTMLImageElement.prototype, 'naturalWidth', 'get').mockReturnValue(
    231
  )
  mockLiveRequest()
  await renderAuthorization(returnTarget, true)
  const account = await screen.findByRole('region', { name: 'Current account' })
  const original = account.querySelector('img')?.getAttribute('src')
  expect(original).toMatch(/^data:image\/svg\+xml/)

  act(() =>
    useAuthStore.getState().auth.setBundle({
      ...bundle,
      user: { ...bundle.user, username: 'renamed', display_name: 'Renamed' },
    })
  )
  expect(account.querySelector('img')).toHaveAttribute('src', original)

  act(() => useAuthStore.getState().auth.setBundle(otherBundle))
  await waitFor(() => {
    expect(account.querySelector('img')).toHaveAttribute(
      'src',
      expect.stringMatching(/^data:image\/svg\+xml/)
    )
    expect(account.querySelector('img')).not.toHaveAttribute('src', original)
  })
  expect(within(account).getByText('other-user')).toBeVisible()
})

it('rejects a missing flow before any request or native sign-in navigation', async () => {
  const get = vi.spyOn(api, 'get')
  const router = await renderAuthorization('/xcode/authorize')
  expect(
    await screen.findByText('Authorization request unavailable')
  ).toBeVisible()
  expect(get).not.toHaveBeenCalled()
  expect(router.state.location.pathname).toBe('/xcode/authorize')
})
