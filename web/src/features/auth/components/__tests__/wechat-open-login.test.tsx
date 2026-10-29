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
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { weChatOpenProofHeaders } from '../../lib/wechat-open-flow'
import { WechatOpenLogin, type WxLoginOptions } from '../wechat-open-login'

const capability = {
  enabled: true,
  app_id: 'wx-approved-app',
  redirect_uri: 'https://gateway.example.com/oauth/wechat-open',
}
const locksDescriptor = Object.getOwnPropertyDescriptor(navigator, 'locks')

beforeEach(() => {
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: { request: vi.fn() },
  })
  useAuthStore.getState().auth.reset('idle')
  sessionStorage.clear()
  vi.spyOn(api, 'post').mockImplementation(async () => ({
    data: {
      success: true,
      data: {
        flow_token: `state-${vi.mocked(api.post).mock.calls.length}`,
        expires_at: Math.floor(Date.now() / 1000) + 600,
      },
    },
  }))
})

afterEach(() => {
  cleanup()
  document
    .querySelectorAll('script[src*="wxLogin.js"]')
    .forEach((node) => node.remove())
  delete window.WxLogin
  useAuthStore.getState().auth.reset('idle')
  sessionStorage.clear()
  if (locksDescriptor) {
    Object.defineProperty(navigator, 'locks', locksDescriptor)
  } else Reflect.deleteProperty(navigator, 'locks')
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('requires legal consent and an explicit click before creating state or loading WeChat', async () => {
  const user = userEvent.setup()
  const view = render(
    <WechatOpenLogin capability={capability} consentGranted={false} />
  )
  expect(
    screen.getByRole('button', { name: 'Start WeChat login' })
  ).toBeDisabled()
  expect(api.post).not.toHaveBeenCalled()
  expect(document.querySelector('script[src*="wxLogin.js"]')).toBeNull()
  view.rerender(<WechatOpenLogin capability={capability} consentGranted />)
  expect(api.post).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Start WeChat login' }))
  await waitFor(() =>
    expect(document.querySelector('script[src*="wxLogin.js"]')).not.toBeNull()
  )
})

it('shows script failure and creates a fresh attempt only after an explicit restart', async () => {
  const user = userEvent.setup()
  render(<WechatOpenLogin capability={capability} consentGranted />)
  await user.click(screen.getByRole('button', { name: 'Start WeChat login' }))
  const script = await waitFor(() => {
    const element = document.querySelector('script[src*="wxLogin.js"]')
    if (!element) throw new Error('WeChat script is not loaded yet')
    return element
  })
  fireEvent.error(script)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Unable to load WeChat login. Please try again or use account login.'
  )
  expect(api.post).toHaveBeenCalledTimes(1)
  expect(() => weChatOpenProofHeaders('state-1')).toThrow()
  await user.click(screen.getByRole('button', { name: 'Restart WeChat login' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(2))
})

it('uses the official website flow and treats readiness as awaiting user confirmation', async ({
  onTestFinished,
}) => {
  let options: WxLoginOptions | undefined
  const sdkMessage = vi.fn()
  onTestFinished(() => window.removeEventListener('message', sdkMessage))
  window.WxLogin = vi.fn(function (value: WxLoginOptions) {
    options = value
    window.addEventListener('message', sdkMessage)
    Object.assign(value, {
      onCleanup: () => window.removeEventListener('message', sdkMessage),
    })
  })
  const user = userEvent.setup()
  render(
    <WechatOpenLogin
      capability={capability}
      consentGranted
      redirectTo='/xcode/authorize?flow_token=original'
    />
  )
  await user.click(screen.getByRole('button', { name: 'Start WeChat login' }))
  await waitFor(() => expect(options).toBeDefined())
  expect(options).toMatchObject({
    appid: capability.app_id,
    scope: 'snsapi_login',
    self_redirect: false,
    state: 'state-1',
  })
  expect(options?.redirect_uri).toBe(
    encodeURIComponent(capability.redirect_uri)
  )
  act(() => options?.onReady?.(true))
  expect(screen.getByRole('status')).toHaveTextContent(
    'Confirm in WeChat to continue'
  )
  expect(useAuthStore.getState().auth.user).toBeNull()
  window.dispatchEvent(new MessageEvent('message'))
  expect(sdkMessage).toHaveBeenCalledOnce()
  act(() => {
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    options?.onReady?.(true)
  })
  expect(() => weChatOpenProofHeaders('state-1')).toThrow()
  window.dispatchEvent(new MessageEvent('message'))
  expect(sdkMessage).toHaveBeenCalledOnce()
  expect(
    screen.getByRole('button', { name: 'Start WeChat login' })
  ).toBeEnabled()
})

it.each([false, true])(
  'invalidates the QR code when the browser account changes (returns to original: %s)',
  async (returnToOriginal) => {
    window.WxLogin = vi.fn(function () {})
    const user = userEvent.setup()
    render(<WechatOpenLogin capability={capability} consentGranted />)
    await user.click(screen.getByRole('button', { name: 'Start WeChat login' }))
    await waitFor(() => expect(window.WxLogin).toHaveBeenCalled())
    act(() => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 42, username: 'new-account', role: 1 })
      if (returnToOriginal) useAuthStore.getState().auth.reset('idle')
    })
    expect(() => weChatOpenProofHeaders('state-1')).toThrow()
    expect(
      screen.getByRole('button', { name: 'Start WeChat login' })
    ).toBeEnabled()
  }
)

it('does not create a server state when browser storage is unavailable', async () => {
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('Storage blocked')
  })
  const user = userEvent.setup()
  render(<WechatOpenLogin capability={capability} consentGranted />)
  await user.click(screen.getByRole('button', { name: 'Start WeChat login' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Unable to store the WeChat login request. Please use account sign-in.'
  )
  expect(api.post).not.toHaveBeenCalled()
})

it('expires the QR code without automatically requesting another state', async () => {
  vi.useFakeTimers()
  vi.mocked(api.post).mockResolvedValue({
    data: {
      success: true,
      data: {
        flow_token: 'expiring-state',
        expires_at: Math.floor(Date.now() / 1000) + 1,
      },
    },
  })
  window.WxLogin = vi.fn(function (options: WxLoginOptions) {
    options.onReady?.(true)
  })
  render(<WechatOpenLogin capability={capability} consentGranted />)
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Start WeChat login' }))
  })
  await act(async () => {
    vi.advanceTimersByTime(1100)
  })
  expect(screen.getByRole('alert')).toHaveTextContent(
    'WeChat login flow expired. Please start again.'
  )
  expect(api.post).toHaveBeenCalledTimes(1)
  expect(() => weChatOpenProofHeaders('expiring-state')).toThrow()
})
