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
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test } from 'vitest'

import { AuthLayout } from '@/features/auth/auth-layout'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { PublicHeader } from '../public-header'

const clients: QueryClient[] = []

beforeEach(() => {
  localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useSystemConfigStore.setState({ loading: false })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

function renderBrandPage(page: 'home' | 'sign-in') {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  clients.push(client)
  client.setQueryData(['status'], { system_name: 'aiscoo' })
  client.setQueryData(['notice'], { success: true, data: '' })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () =>
        page === 'home' ? (
          <PublicHeader
            showNotifications={false}
            showLanguageSwitcher={false}
            showThemeSwitch={false}
          />
        ) : (
          <AuthLayout title='Sign in'>
            <p>Account form</p>
          </AuthLayout>
        ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

test.each(['home', 'sign-in'] as const)(
  '%s displays the complete animated wordmark without a second visible brand label',
  async (page) => {
    renderBrandPage(page)
    const brand = await screen.findByRole('link', {
      name: 'aiscoo',
    })
    expect(
      brand.querySelector('img[src="/brand/wordmark-animated-light.svg"]')
    ).not.toBeNull()
    expect(
      brand.querySelector('img[src="/brand/wordmark-animated-dark.svg"]')
    ).not.toBeNull()
    expect(brand.querySelector('img[src="/brand/mark-light.svg"]')).toBeNull()
    expect(brand.querySelector('img[src="/brand/mark-dark.svg"]')).toBeNull()
    for (const label of brand.querySelectorAll('h1, span[title]')) {
      expect(label).toHaveClass('sr-only')
    }
  }
)

test('the animated header offers a static wordmark when reduced motion is requested', async () => {
  renderBrandPage('home')
  const brand = await screen.findByRole('link', { name: 'aiscoo' })
  const sources = [
    ...brand.querySelectorAll(
      'source[media="(prefers-reduced-motion: reduce)"]'
    ),
  ]
  expect(sources.map((source) => source.getAttribute('srcset'))).toEqual([
    '/brand/wordmark-light.svg',
    '/brand/wordmark-dark.svg',
  ])
})

test.each(['home', 'sign-in'] as const)(
  '%s retains a configured custom logo and site name',
  async (page) => {
    useSystemConfigStore
      .getState()
      .setConfig({ systemName: 'Research Hub', logo: '/custom.svg' })
    useSystemConfigStore.getState().setLoadedLogoUrl('/custom.svg')
    renderBrandPage(page)
    const logo = await screen.findByRole('img', { name: 'Research Hub' })
    expect(logo).toHaveAttribute('src', '/custom.svg')
    expect(screen.getByText('Research Hub')).not.toHaveClass('sr-only')
  }
)
