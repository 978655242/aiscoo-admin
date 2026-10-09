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
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
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
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { consumeOAuthLoginRedirect } from '@/features/auth/lib/oauth-callback-mode'
import { SignIn } from '@/features/auth/sign-in'
import { SignUp } from '@/features/auth/sign-up'
import type { SystemStatus } from '@/features/auth/types'
import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

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
vi.mock('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    systemName: 'New API',
    logo: '/logo.png',
    loading: false,
  }),
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
let client: QueryClient
const openClient = vi.fn()

async function renderAuthorization(
  initialEntry = returnTarget,
  authenticated = false
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
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return router
}

function mockLiveRequest(expiresAt = Date.now() + 600000) {
  return vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        schema_version: 1,
        server_time: Date.now(),
        client_id: 'xcode-desktop',
        expires_at: expiresAt,
      },
    },
  })
}

beforeEach(() => {
  useAuthStore.getState().auth.reset('complete')
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  window.sessionStorage.clear()
  openClient.mockReset()
  vi.stubGlobal('location', { ...window.location, assign: openClient })
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset('idle')
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
    screen.queryByRole('button', { name: 'Authorize XCode' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Sign in' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Create account' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
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
  await screen.findByRole('heading', { name: 'Authorize XCode' })
  expect(router.state.location.href).toBe(returnTarget)
  expect(screen.getByText('native-user')).toBeVisible()
  expect(screen.getByText('xcode-desktop')).toBeVisible()
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
  await screen.findByRole('heading', { name: 'Authorize XCode' })
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

it('requires explicit confirmation and spends a failed one-use authorization only once without replay', async () => {
  mockLiveRequest()
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValue(new Error('Network interrupted'))
  await renderAuthorization(returnTarget, true)
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Authorize XCode' })
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(/native-user/)).toBeVisible()
  expect(
    within(dialog).getByText(
      'XCode can read your profile, account plans and account usage, and run inference with your designated XCode plan.'
    )
  ).toBeVisible()
  expect(
    within(dialog).getByText(
      'XCode cannot manage payments, API keys or account security.'
    )
  ).toBeVisible()
  expect(
    within(dialog).getByText(
      'The device session follows the native New API session lifetime (30 days by default). You can revoke the XCode device in Login Sessions.'
    )
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
  await user.dblClick(
    within(dialog).getByRole('button', { name: 'Authorize and open XCode' })
  )
  expect(
    await screen.findByText('Authorization could not be completed')
  ).toBeVisible()
  expect(post).toHaveBeenCalledTimes(1)
  expect(post).toHaveBeenCalledWith(
    '/api/xcode/v1/auth/authorize/complete',
    { flow_token: 'validated-flow' },
    expect.objectContaining({ singleUseAuthorization: true })
  )
  await act(async () => {
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('online'))
  })
  expect(post).toHaveBeenCalledTimes(1)
  expect(
    screen.queryByRole('button', { name: /Retry|Authorize/ })
  ).not.toBeInTheDocument()
  expect(useAuthStore.getState().auth.session?.sid).toBe(
    'native-browser-session'
  )
})

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
      await screen.findByRole('button', { name: 'Authorize XCode' })
    )
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Authorize and open XCode',
      })
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

it('opens only the validated callback once and never stores the authorization code or XCode credentials', async () => {
  mockLiveRequest()
  const callback =
    'zcode://oauth/callback?provider=new-api&code=one-use-code&state=host-state'
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
    await screen.findByRole('button', { name: 'Authorize XCode' })
  )
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Authorize and open XCode',
    })
  )
  await screen.findByText('Opening XCode...')
  expect(openClient).toHaveBeenCalledExactlyOnceWith(callback)
  expect(post).toHaveBeenCalledTimes(1)
  expect({ ...window.localStorage }).toEqual(localBefore)
  expect({ ...window.sessionStorage }).toEqual(sessionBefore)
  expect(useAuthStore.getState().auth.session?.sid).toBe(
    'native-browser-session'
  )
  expect(useAuthStore.getState().auth.accessToken).toBe('native-browser-access')
  expect(screen.queryByText(/one-use-code|host-state/)).not.toBeInTheDocument()
})

it('rejects confirmation if the signed-in native session changed after opening the dialog', async () => {
  mockLiveRequest()
  const post = vi.spyOn(api, 'post')
  await renderAuthorization(returnTarget, true)
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Authorize XCode' })
  )
  await act(async () =>
    useAuthStore.getState().auth.setBundle({
      ...bundle,
      session: { ...bundle.session, sid: 'other-native-session' },
    })
  )
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Authorize and open XCode',
    })
  )
  expect(
    await screen.findByText('Authorization could not be completed')
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
  expect(openClient).not.toHaveBeenCalled()
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
