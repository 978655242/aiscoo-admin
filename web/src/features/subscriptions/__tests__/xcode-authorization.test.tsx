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
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import { api } from '@/lib/api'

import { UserSubscriptionsDialog } from '../components/dialogs/user-subscriptions-dialog'
import { XCodeAuthorizationSection } from '../components/dialogs/xcode-authorization-section'
import type { PlanRecord, XCodeAuthorizationSnapshot } from '../types'

const snapshot = {
  schema_version: 1,
  server_time: 1791400000000,
  status: 'active',
  authorization_revision: '9007199254740993',
  subscription: {
    id: '9007199254740995',
    plan_id: '4',
    plan_title: 'Designated plan',
    source: 'admin',
    usage_scope: 'xcode',
    start_time: 1791300000000,
    end_time: 1794000000000,
  },
  quota: {
    scope: 'xcode-subscription',
    unit: 'quota',
    unlimited: false,
    total: '9007199254740993',
    used: '7',
    remaining: '9007199254740986',
    used_percentage: 0.01,
    reset_period: 'custom',
    reset_custom_seconds: '5401',
    last_reset_time: 1791390000000,
    next_reset_time: 1791395401000,
    reset_time_zone: 'server-local',
    reset_utc_offset_seconds: 28800,
  },
  funding_policy: {
    subscription_only: true,
    wallet_fallback: false,
    other_subscription_fallback: false,
  },
  self_service_reset: false,
  purchase_mode: 'manual_admin',
} satisfies XCodeAuthorizationSnapshot
const plans: PlanRecord[] = [
  {
    plan: {
      id: 4,
      title: 'Designated plan',
      price_amount: 10,
      currency: 'USD',
      duration_unit: 'month',
      duration_value: 1,
      quota_reset_period: 'monthly',
      enabled: true,
      sort_order: 0,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 500000,
    },
  },
  {
    plan: {
      id: 5,
      title: 'Replacement plan',
      price_amount: 20,
      currency: 'USD',
      duration_unit: 'day',
      duration_value: 3,
      quota_reset_period: 'daily',
      enabled: true,
      sort_order: 1,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 1000000,
    },
  },
]
const clients: QueryClient[] = []
function mount(
  data: XCodeAuthorizationSnapshot | Error = snapshot,
  availablePlans = plans
) {
  const get = vi.spyOn(api, 'get')
  if (data instanceof Error) get.mockRejectedValue(data)
  else get.mockResolvedValue({ data: { success: true, data } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  clients.push(client)
  const refresh = vi.fn(async () => {
    await client.invalidateQueries({
      queryKey: ['xcode-admin-authorization', 7],
    })
  })
  render(
    <QueryClientProvider client={client}>
      <XCodeAuthorizationSection
        userId={7}
        plans={availablePlans}
        plansReady
        onRefresh={refresh}
      />
    </QueryClientProvider>
  )
  return { refresh }
}

function mountDrawer() {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/subscription/admin/plans') {
      return { data: { success: true, data: plans } }
    }
    if (url === '/api/subscription/admin/users/7/subscriptions') {
      return {
        data: {
          success: true,
          data: [
            {
              subscription: {
                id: 11,
                user_id: 7,
                plan_id: 4,
                status: 'active',
                source: 'admin',
                usage_scope: 'xcode',
                start_time: 1791300000,
                end_time: 4102444800,
                amount_total: 500000,
                amount_used: 7,
              },
            },
          ],
        },
      }
    }
    if (url === '/api/xcode/v1/admin/users/7/authorization') {
      return { data: { success: true, data: snapshot } }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <UserSubscriptionsDialog
          open
          user={{ id: 7 }}
          onOpenChange={() => {}}
        />
      </TooltipProvider>
    </QueryClientProvider>
  )
  return get
}
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

describe('aiscoo designated authorization', () => {
  test('failed GET shows an error and never offers unassigned grant actions', async () => {
    mount(new Error('Database unavailable'))
    await screen.findByText('Unable to load aiscoo authorization')
    expect(
      screen.queryByRole('button', { name: 'Grant authorization' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
  })

  test('large quota values stay exact and custom cycles are not relabeled as whole hours', async () => {
    mount()
    await screen.findByText('9007199254740993 quota')
    expect(screen.getByText('5401 seconds')).toBeInTheDocument()
    expect(screen.getByText(/server-local/)).toBeInTheDocument()
  })

  test('unlimited authorization shows unlimited instead of zero while preserving used quota', async () => {
    mount({
      ...snapshot,
      quota: {
        ...snapshot.quota,
        unlimited: true,
        total: null,
        remaining: null,
        used_percentage: null,
      },
    })
    expect(await screen.findAllByText('Unlimited')).toHaveLength(2)
    expect(screen.getByText('Used')).toBeInTheDocument()
  })

  test('reset explicitly posts false with the exact current revision and refreshes state after success', async () => {
    const { refresh } = mount()
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: { ...snapshot, authorization_revision: '9007199254740994' },
      },
    })
    await userEvent.click(
      await screen.findByRole('button', { name: 'Reset designated quota' })
    )
    const dialog = screen.getByRole('alertdialog')
    await userEvent.click(
      within(dialog).getByRole('switch', { name: 'Advance next reset time' })
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Reset designated quota' })
    )
    await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
    expect(post).toHaveBeenCalledWith(
      '/api/xcode/v1/admin/users/7/authorization',
      {
        action: 'reset',
        expected_revision: '9007199254740993',
        advance_reset_time: false,
      },
      expect.anything()
    )
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  test.each([409, 503])(
    '%s closes uncertain confirmation and refreshes without replaying the mutation',
    async (status) => {
      const { refresh } = mount()
      const post = vi.spyOn(api, 'post').mockRejectedValue({
        response: {
          status,
          data: {
            success: false,
            message: 'Authorization changed',
            code: 'XCODE_AUTHORIZATION_CONFLICT',
          },
        },
      })
      await userEvent.click(
        await screen.findByRole('button', { name: 'Renew authorization' })
      )
      vi.mocked(api.get).mockResolvedValue({
        data: {
          success: true,
          data: { ...snapshot, authorization_revision: '9007199254740994' },
        },
      })
      await userEvent.click(
        within(screen.getByRole('alertdialog')).getByRole('button', {
          name: 'Renew authorization',
        })
      )
      await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
      await screen.findByText('Revision: 9007199254740994')
      expect(post).toHaveBeenCalledOnce()
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    }
  )

  test('failed business response keeps confirmation open without claiming success or refreshing', async () => {
    const { refresh } = mount()
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: false, message: 'Compliance required' },
    })
    await userEvent.click(
      await screen.findByRole('button', { name: 'Revoke authorization' })
    )
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Revoke authorization',
      })
    )
    await waitFor(() => expect(post).toHaveBeenCalledOnce())
    await waitFor(() =>
      expect(
        within(screen.getByRole('alertdialog')).getByRole('button', {
          name: 'Revoke authorization',
        })
      ).toBeEnabled()
    )
    expect(refresh).not.toHaveBeenCalled()
  })

  test('revoked authorization permits a new grant but never renewal', async () => {
    mount({ ...snapshot, status: 'revoked' })
    expect(
      await screen.findByRole('button', { name: 'Grant authorization' })
    ).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Renew authorization' })
    ).not.toBeInTheDocument()
  })

  test('plan selection works by keyboard and enables replacement only for a different plan', async () => {
    mount()
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: snapshot } })
    const select = await screen.findByRole('combobox', {
      name: 'aiscoo authorization plan',
    })
    expect(
      screen.getByRole('button', { name: 'Replace authorization' })
    ).toBeDisabled()
    await userEvent.clear(select)
    await userEvent.type(select, 'Replacement')
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(select).toHaveValue('Replacement plan')
    expect(
      screen.getByRole('button', { name: 'Replace authorization' })
    ).toBeEnabled()
    expect(
      screen.getByRole('button', { name: 'Renew authorization' })
    ).toBeDisabled()
    await userEvent.click(
      screen.getByRole('button', { name: 'Replace authorization' })
    )
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Replace authorization',
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(post).toHaveBeenCalledWith(
      '/api/xcode/v1/admin/users/7/authorization',
      {
        action: 'replace',
        expected_revision: '9007199254740993',
        plan_id: '5',
      },
      expect.anything()
    )
  })

  test.each([
    {
      status: 'revoked' as const,
      label: 'Grant authorization',
      body: {
        action: 'grant',
        expected_revision: '9007199254740993',
        plan_id: '4',
      },
    },
    {
      status: 'active' as const,
      label: 'Renew authorization',
      body: {
        action: 'renew',
        expected_revision: '9007199254740993',
        plan_id: '4',
      },
    },
    {
      status: 'active' as const,
      label: 'Revoke authorization',
      body: { action: 'revoke', expected_revision: '9007199254740993' },
    },
  ])(
    '$label submits its exact CAS body only after confirmation',
    async ({ status, label, body }) => {
      const { refresh } = mount({ ...snapshot, status })
      const post = vi
        .spyOn(api, 'post')
        .mockResolvedValue({ data: { success: true, data: snapshot } })
      await userEvent.click(await screen.findByRole('button', { name: label }))
      expect(post).not.toHaveBeenCalled()
      await userEvent.click(
        within(screen.getByRole('alertdialog')).getByRole('button', {
          name: label,
        })
      )
      await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
      expect(post).toHaveBeenCalledWith(
        '/api/xcode/v1/admin/users/7/authorization',
        body,
        expect.anything()
      )
    }
  )

  test('pending reset disables confirmation, cancellation, and the explicit switch', async () => {
    mount()
    let resolvePost!: (value: {
      data: { success: true; data: XCodeAuthorizationSnapshot }
    }) => void
    // The project's ES2022 library does not declare Promise.withResolvers.
    vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve
        })
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Reset designated quota' })
    )
    const dialog = screen.getByRole('alertdialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Reset designated quota' })
    )
    expect(
      within(dialog).getByRole('button', { name: 'Reset designated quota' })
    ).toBeDisabled()
    expect(
      within(dialog).getByRole('button', { name: 'Cancel' })
    ).toBeDisabled()
    const resetSwitch = within(dialog).getByRole('switch', {
      name: 'Advance next reset time',
    })
    await userEvent.click(resetSwitch)
    expect(resetSwitch).toBeChecked()
    resolvePost({ data: { success: true, data: snapshot } })
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
  })

  test('disabled and purchase-limited templates cannot be selected for authorization', async () => {
    mount(snapshot, [
      ...plans,
      {
        plan: {
          ...plans[0].plan,
          id: 6,
          title: 'Disabled template',
          enabled: false,
        },
      },
      {
        plan: {
          ...plans[0].plan,
          id: 7,
          title: 'Purchase-limited template',
          max_purchase_per_user: 1,
        },
      },
    ])
    const select = await screen.findByRole('combobox', {
      name: 'aiscoo authorization plan',
    })
    select.focus()
    await userEvent.keyboard('{ArrowDown}')
    expect(
      await screen.findByRole('option', { name: 'Replacement plan' })
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: 'Disabled template' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: 'Purchase-limited template' })
    ).not.toBeInTheDocument()
  })

  test('managed rows block hard deletion but allow cancellation', async () => {
    mountDrawer()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Actions' })
    )
    expect(
      await screen.findByRole('menuitem', { name: 'Delete' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('menuitem', { name: 'Invalidate' })
    ).not.toHaveAttribute('aria-disabled', 'true')
  })
})
