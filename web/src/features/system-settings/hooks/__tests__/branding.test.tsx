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
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios'
import { useState, type ReactNode } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { HeaderLogo } from '@/components/layout/components/header-logo'
import { useHomePageContent } from '@/features/home/hooks/use-home-page-content'
import { useSystemConfig } from '@/hooks/use-system-config'
import { api } from '@/lib/api'
import { statusQueryOptions } from '@/lib/status-query'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemInfoSection } from '../../general/system-info-section'
import { useUpdateOption } from '../use-update-option'

let client: QueryClient

function Wrapper(props: { children: ReactNode }) {
  return (
    <QueryClientProvider client={client}>{props.children}</QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

test.each([
  'SystemName',
  'Logo',
  'Footer',
  'general_setting.docs_link',
  'passkey.rp_display_name',
])(
  'saving %s refreshes the visible status and persisted config',
  async (key) => {
    let name = 'Before'
    vi.spyOn(api, 'get').mockImplementation(async () => ({
      data: {
        success: true,
        data: {
          system_name: name,
          logo: '',
          footer_html: name,
          docs_link: name,
        },
      },
    }))
    vi.spyOn(api, 'put').mockImplementation(async () => {
      name = 'After'
      return { data: { success: true, message: '' } }
    })
    const hook = renderHook(
      () => ({
        status: useQuery(statusQueryOptions),
        update: useUpdateOption(),
      }),
      { wrapper: Wrapper }
    )
    await waitFor(() =>
      expect(hook.result.current.status.data?.system_name).toBe('Before')
    )
    await act(() =>
      hook.result.current.update.mutateAsync({ key, value: 'After' })
    )
    await waitFor(() =>
      expect(hook.result.current.status.data?.system_name).toBe('After')
    )
    expect(useSystemConfigStore.getState().config.systemName).toBe('After')
    expect(JSON.parse(localStorage.getItem('status') || '{}').system_name).toBe(
      'After'
    )
  }
)

test('saving branding ignores a status read started before the edit', async () => {
  let name = 'Before'
  let resolveOld!: () => void
  let started = false
  let holdOld = false
  const adapter = async (
    config: InternalAxiosRequestConfig
  ): Promise<AxiosResponse> => {
    if (config.method === 'put') {
      name = 'After'
      return {
        data: { success: true, message: '' },
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
      }
    }
    const snapshot = name
    if (holdOld && snapshot === 'Before') {
      started = true
      await new Promise<void>((resolve) => {
        resolveOld = resolve
      })
    }
    return {
      data: { success: true, data: { system_name: snapshot } },
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
    }
  }
  const oldAdapter = api.defaults.adapter
  api.defaults.adapter = adapter
  try {
    const hook = renderHook(
      () => ({
        status: useQuery(statusQueryOptions),
        update: useUpdateOption(),
      }),
      { wrapper: Wrapper }
    )
    await waitFor(() =>
      expect(hook.result.current.status.data?.system_name).toBe('Before')
    )
    holdOld = true
    const oldRead = client.refetchQueries({ queryKey: ['status'] })
    await waitFor(() => expect(started).toBe(true))
    await act(() =>
      hook.result.current.update.mutateAsync({
        key: 'SystemName',
        value: 'After',
      })
    )
    await act(async () => {
      resolveOld()
      await oldRead
    })
    await waitFor(() =>
      expect(hook.result.current.status.data?.system_name).toBe('After')
    )
    expect(useSystemConfigStore.getState().config.systemName).toBe('After')
  } finally {
    api.defaults.adapter = oldAdapter
  }
})

function BrandFormFixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <SystemInfoSection
          defaultValues={{
            SystemName: 'aiscoo',
            Logo: '/brand/app-light.svg',
            ServerAddress: '',
            TaskPublicAddress: '',
            Footer: '',
            About: '',
            HomePageContent: '',
            general_setting: { docs_link: '' },
            legal: { user_agreement: '', privacy_policy: '' },
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

test('the default bundled Logo does not prevent saving other site settings', async () => {
  const writes: unknown[] = []
  vi.spyOn(api, 'put').mockImplementation(async (_url, data) => {
    writes.push(data)
    return { data: { success: true, message: '' } }
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: BrandFormFixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <Wrapper>
      <RouterProvider router={router} />
    </Wrapper>
  )
  const name = await screen.findByRole('textbox', { name: 'System Name' })
  fireEvent.change(name, { target: { value: 'Research Hub' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(writes).toContainEqual({ key: 'SystemName', value: 'Research Hub' })
  )
})

test('successful About edits refresh the content query', async () => {
  let content = 'Before'
  vi.spyOn(api, 'put').mockImplementation(async () => {
    content = 'After'
    return { data: { success: true, message: '' } }
  })
  const hook = renderHook(
    () => ({
      about: useQuery({
        queryKey: ['about-content'],
        queryFn: async () => content,
      }),
      update: useUpdateOption(),
    }),
    { wrapper: Wrapper }
  )
  await waitFor(() => expect(hook.result.current.about.data).toBe('Before'))
  await act(() =>
    hook.result.current.update.mutateAsync({ key: 'About', value: 'After' })
  )
  await waitFor(() => expect(hook.result.current.about.data).toBe('After'))
})

test('successful homepage edits refresh mounted content and its cached snapshot', async () => {
  let content = 'Before'
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: content },
  }))
  vi.spyOn(api, 'put').mockImplementation(async () => {
    content = 'After'
    return { data: { success: true, message: '' } }
  })
  const hook = renderHook(
    () => ({ home: useHomePageContent(), update: useUpdateOption() }),
    { wrapper: Wrapper }
  )
  await waitFor(() => expect(hook.result.current.home.content).toBe('Before'))
  await act(() =>
    hook.result.current.update.mutateAsync({
      key: 'HomePageContent',
      value: 'After',
    })
  )
  await waitFor(() => expect(hook.result.current.home.content).toBe('After'))
  expect(localStorage.getItem('home_page_content')).toBe('After')
})

test('saving homepage content wins over an older pending read', async () => {
  let resolveRead!: (response: {
    data: { success: boolean; data: string }
  }) => void
  vi.spyOn(api, 'get').mockImplementation(
    () =>
      new Promise((resolve) => {
        resolveRead = resolve
      })
  )
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, message: '' },
  })
  const hook = renderHook(
    () => ({ home: useHomePageContent(), update: useUpdateOption() }),
    { wrapper: Wrapper }
  )
  await act(() =>
    hook.result.current.update.mutateAsync({
      key: 'HomePageContent',
      value: 'After',
    })
  )
  await act(async () => {
    resolveRead({ data: { success: true, data: 'Before' } })
  })
  expect(hook.result.current.home.content).toBe('After')
  expect(localStorage.getItem('home_page_content')).toBe('After')
})

test('a failed custom Logo falls back to themed aiscoo artwork', () => {
  const view = render(
    <HeaderLogo
      src='/missing.svg'
      alt='Research Hub'
      loading={false}
      logoLoaded
      className='size-7'
    />
  )
  fireEvent.error(screen.getByRole('img', { name: 'Research Hub' }))
  expect(
    view.container.querySelector('img[src="/brand/mark-light.svg"]')
  ).not.toBeNull()
  expect(
    view.container.querySelector('img[src="/brand/mark-dark.svg"]')
  ).not.toBeNull()
})

test('brand changes update browser metadata without a page reload', async () => {
  document.head.innerHTML = '<meta name="title" content="Before" />'
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { system_name: 'Before' } },
  })
  renderHook(() => useSystemConfig({ autoLoad: true }), { wrapper: Wrapper })
  await waitFor(() => expect(document.title).toBe('Before'))
  act(() =>
    useSystemConfigStore
      .getState()
      .setConfig({ systemName: 'After', logo: '/custom.svg' })
  )
  await waitFor(() => expect(document.title).toBe('After'))
  expect(
    document.querySelector('meta[name="title"]')?.getAttribute('content')
  ).toBe('After')
})
