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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { SignIn } from '..'

afterEach(cleanup)

it('keeps explicit legal consent without repeating legal links in a sign-in footer', async () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(STATUS_QUERY_KEY, {
    password_login_enabled: true,
    register_enabled: true,
    user_agreement_enabled: true,
    privacy_policy_enabled: true,
  })
  const root = createRootRoute({ component: Outlet })
  const auth = createRoute({
    getParentRoute: () => root,
    id: '(auth)',
    component: Outlet,
  })
  const signIn = createRoute({
    getParentRoute: () => auth,
    path: '/sign-in',
    component: SignIn,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([signIn])]),
    history: createMemoryHistory({ initialEntries: ['/sign-in'] }),
  })
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )

  try {
    const consent = await screen.findByRole('checkbox', {
      name: /I have read and agree to the/,
    })
    expect(consent).not.toBeChecked()
    // 原因：表单已提供显式协议同意，卡片底部的再次提示会重复展示同一组链接。
    expect(
      screen.queryByText(/By clicking sign in, you agree to our/)
    ).toBeNull()
    expect(
      screen.getAllByRole('link', { name: 'User Agreement' })
    ).toHaveLength(1)
    expect(
      screen.getAllByRole('link', { name: 'Privacy Policy' })
    ).toHaveLength(1)
    await user.click(consent)
    expect(consent).toBeChecked()
  } finally {
    view.unmount()
    queryClient.clear()
  }
})
