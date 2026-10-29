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
import { act, cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, assert, beforeEach, expect, it, vi } from 'vitest'

import { ThemeProvider } from '@/context/theme-provider'
import { api } from '@/lib/api'

import { XCodePlans } from '../plans'
import type { XCodeWebPlan, XCodeWebView } from '../plans-api'

type Presentation = XCodeWebView['plan_presentations'][number]
const now = Date.UTC(2026, 9, 11)
const end = Date.UTC(2030, 6, 14, 12)
const metadata: NonNullable<Presentation['metadata']> = {
  group_key: 'pro',
  group_title: 'Pro',
  offer_kind: 'standard',
  highlights: ['For daily coding'],
  features: ['Native model access'],
}

function plan(id: string, overrides: Partial<XCodeWebPlan> = {}): XCodeWebPlan {
  return {
    id,
    title: `Pro template ${id}`,
    subtitle: '',
    price_amount: '10',
    currency: 'USD',
    duration_unit: 'month',
    duration_value: '1',
    duration_custom_seconds: '0',
    total_quota: '1000',
    reset_period: 'monthly',
    reset_custom_seconds: '0',
    enabled: true,
    ...overrides,
  }
}

function view(): XCodeWebView {
  const items = [
    plan('101', { title: 'Pro Monthly' }),
    plan('102', {
      title: 'Pro Quarterly',
      duration_value: '3',
      price_amount: '24',
    }),
    plan('104', {
      title: 'Pro Annual',
      duration_unit: 'year',
      price_amount: '96',
    }),
  ]
  return {
    schema_version: 1,
    server_time: now,
    plans: {
      schema_version: 1,
      server_time: now,
      purchase_mode: 'manual_admin',
      self_service_purchase: false,
      items,
    },
    plan_presentations: items.map((item) => ({
      plan_id: item.id,
      metadata,
      assignable: true,
      assignment_reason: null,
      display_issue: null,
    })),
    entitlements: {
      schema_version: 1,
      server_time: now,
      status: 'active',
      authorization_revision: '8',
      subscription: {
        id: '9001',
        plan_id: '104',
        plan_title: 'Granted annual snapshot',
        source: 'admin',
        usage_scope: 'xcode',
        start_time: Date.UTC(2026, 0, 1),
        end_time: end,
      },
      quota: {
        scope: 'xcode-subscription',
        unit: 'quota',
        unlimited: false,
        total: '1000',
        used: '400',
        remaining: '600',
        used_percentage: 40,
        reset_period: 'monthly',
        reset_custom_seconds: '0',
        last_reset_time: Date.UTC(2026, 9, 1),
        next_reset_time: Date.UTC(2026, 10, 1),
        reset_time_zone: 'UTC',
        reset_utc_offset_seconds: 0,
      },
      funding_policy: {
        subscription_only: true,
        wallet_fallback: false,
        other_subscription_fallback: false,
      },
      self_service_reset: false,
      purchase_mode: 'manual_admin',
    },
    display: {
      quota_per_unit: '500000',
      display_type: 'CNY',
      currency: 'CNY',
      currency_symbol: '¥',
      usd_to_currency_rate: '7',
    },
  }
}

let client: QueryClient
const requests: { url: string; method: string }[] = []
const originalAdapter = api.defaults.adapter

function renderPlans(
  snapshot: XCodeWebView,
  audience: 'personal' | 'team' = 'personal'
) {
  document.documentElement.dataset.xcodePlanAudience = audience
  document.documentElement.dataset.xcodePlanTheme = 'light'
  document.documentElement.dataset.xcodePlanLocale = 'en-US'
  document.documentElement.dataset.xcodePlanFontSize = '14'
  window.history.replaceState(null, '', '/xcode/plans')
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input)
      const method = (
        init?.method ?? (input instanceof Request ? input.method : 'GET')
      ).toUpperCase()
      requests.push({ url, method })
      if (url !== '/api/xcode/v1/web/view' || method !== 'GET') {
        throw new Error(`Unexpected viewer request: ${method} ${url}`)
      }
      return new Response(JSON.stringify({ success: true, data: snapshot }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    })
  )
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ThemeProvider defaultTheme='light'>
        <XCodePlans />
      </ThemeProvider>
    </QueryClientProvider>
  )
}

beforeEach(async () => {
  requests.length = 0
  window.localStorage.clear()
  await i18next.changeLanguage('en')
  api.defaults.adapter = async (config) => {
    requests.push({
      url: config.url ?? '',
      method: (config.method ?? 'GET').toUpperCase(),
    })
    throw new Error(
      'The read-only viewer must not call a native management API'
    )
  }
})

afterEach(() => {
  cleanup()
  client?.clear()
  api.defaults.adapter = originalAdapter
  vi.unstubAllGlobals()
  delete document.documentElement.dataset.xcodePlanAudience
  delete document.documentElement.dataset.xcodePlanTheme
  delete document.documentElement.dataset.xcodePlanLocale
  delete document.documentElement.dataset.xcodePlanFontSize
  window.history.replaceState(null, '', '/')
})

it('selects team on the first content frame without briefly rendering personal products or enterprise actions', async () => {
  const rendered = renderPlans(view(), 'team')
  expect(screen.getByRole('button', { name: 'Team' })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
  expect(screen.queryByRole('article')).not.toBeInTheDocument()
  const productFrames: string[] = []
  const observer = new MutationObserver(() => {
    if (rendered.container.querySelector('article')) {
      productFrames.push(rendered.container.textContent ?? '')
    }
  })
  observer.observe(rendered.container, { childList: true, subtree: true })
  try {
    expect(
      await screen.findByText(
        /does not provide team plans or organization features/i
      )
    ).toBeVisible()
    expect(productFrames).toEqual([])
    expect(
      screen.queryByRole('button', { name: /Contact administrator/i })
    ).not.toBeInTheDocument()
  } finally {
    observer.disconnect()
  }
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Personal' }))
  expect(await screen.findByRole('article', { name: /Pro/ })).toBeVisible()
  expect(requests).toEqual([{ url: '/api/xcode/v1/web/view', method: 'GET' }])
})

it('marks the current template card with its status and real end time instead of a header summary', async () => {
  const snapshot = view()
  renderPlans(snapshot)
  const card = await screen.findByRole('article', { name: /Pro/ })
  expect(screen.getByRole('button', { name: 'Yearly' })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
  expect(within(card).getByText('Active', { exact: true })).toBeVisible()
  expect(card).toHaveTextContent(/Expires at:.*2030/)
  expect(
    screen.queryByRole('region', { name: 'aiscoo designated authorization' })
  ).not.toBeInTheDocument()
  expect(screen.queryByText(/not a checkout offer/i)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: 'Monthly' }))
  const monthly = screen.getByRole('article', { name: /Pro/ })
  expect(monthly).not.toHaveTextContent(/Active|Expires at/)
})

it('shows the current instance even when its disabled template is absent from enabled plans', async () => {
  const snapshot = view()
  snapshot.plans.items = snapshot.plans.items.filter(
    (item) => item.id !== '104'
  )
  snapshot.plan_presentations = snapshot.plan_presentations.filter(
    (item) => item.plan_id !== '104'
  )
  renderPlans(snapshot)
  const summary = await screen.findByRole('region', {
    name: 'aiscoo designated authorization',
  })
  expect(summary).toHaveTextContent('Granted annual snapshot')
  expect(summary).toHaveTextContent('2030')
  expect(summary).toHaveTextContent('Active')
  expect(summary).toHaveTextContent('Expires at')
  for (const card of screen.getAllByRole('article')) {
    expect(card).not.toHaveTextContent(/Active|Expires at/)
  }
})

it.each(['active', 'exhausted', 'expired', 'revoked'] as const)(
  'renders %s as its own status on the current card rather than inferring trial or expiry',
  async (status) => {
    const snapshot = view()
    snapshot.entitlements.status = status
    if (status === 'exhausted') {
      assert(snapshot.entitlements.quota)
      snapshot.entitlements.quota = {
        ...snapshot.entitlements.quota,
        used: '1000',
        remaining: '0',
        used_percentage: 100,
      }
    }
    renderPlans(snapshot)
    const card = await screen.findByRole('article', { name: /Pro/ })
    const label = status[0].toUpperCase() + status.slice(1)
    expect(within(card).getByText(label, { exact: true })).toBeVisible()
    expect(
      within(card).queryByText(/trial (ended|expired)/i)
    ).not.toBeInTheDocument()
    if (status === 'exhausted') {
      expect(
        within(card).queryByText('Expired', { exact: true })
      ).not.toBeInTheDocument()
    }
  }
)

it('shows no authorization status or expiry when unassigned', async () => {
  const snapshot = view()
  snapshot.entitlements.status = 'unassigned'
  snapshot.entitlements.subscription = null
  snapshot.entitlements.quota = null
  renderPlans(snapshot)
  await screen.findByRole('article', { name: /Pro/ })
  expect(screen.queryByText(/Unassigned|Expires at/)).not.toBeInTheDocument()
})

it.each([
  ['unassigned', 'Monthly', /grant.*new authorization instance/i],
  ['revoked', 'Yearly', /grant.*new authorization instance/i],
  ['expired', 'Yearly', /renew.*expired.*new authorization instance/i],
  ['expired', 'Monthly', /grant.*new authorization instance/i],
] as const)(
  'explains %s %s using real administrator transition semantics',
  async (status, cycle, action) => {
    const snapshot = view()
    snapshot.entitlements.status = status
    if (status === 'unassigned') {
      snapshot.entitlements.subscription = null
      snapshot.entitlements.quota = null
    }
    renderPlans(snapshot)
    await screen.findByRole('article', { name: /Pro/ })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: cycle }))
    await user.click(
      within(screen.getByRole('article', { name: /Pro/ })).getByRole('button', {
        name: 'Contact administrator',
      })
    )
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent(action)
    expect(dialog).not.toHaveTextContent(
      /replace with this exact template|only extend the term/i
    )
  }
)

it('marks only the exact current SKU among duplicate monthly templates, not the same title or group', async () => {
  const snapshot = view()
  assert(snapshot.entitlements.subscription)
  snapshot.entitlements.subscription = {
    ...snapshot.entitlements.subscription,
    plan_id: '101',
  }
  snapshot.plans.items.push(plan('103', { title: 'Pro Monthly Alternate' }))
  snapshot.plan_presentations.push({
    ...snapshot.plan_presentations[0],
    plan_id: '103',
  })
  renderPlans(snapshot)
  await screen.findAllByRole('article', { name: /Pro Monthly/ })
  const current = screen.getByRole('article', { name: /Pro Monthly.*101/ })
  const alternate = screen.getByRole('article', {
    name: /Pro Monthly Alternate.*103/,
  })
  expect(within(current).getByText('Active', { exact: true })).toBeVisible()
  expect(current).toHaveTextContent(/Expires at:.*2030/)
  expect(alternate).not.toHaveTextContent(/Active|Expires at/)
  expect(screen.getAllByText('Active', { exact: true })).toHaveLength(1)
})

it('does not infer a trial card or trial-ended history from a zero-priced standard plan', async () => {
  const snapshot = view()
  snapshot.plans.items = [plan('101', { price_amount: '0' })]
  snapshot.plan_presentations = [snapshot.plan_presentations[0]]
  renderPlans(snapshot)
  const card = await screen.findByRole('article', { name: /Pro/ })
  expect(card).not.toHaveTextContent(/trial|expired|ended/i)
  expect(
    within(card).getByRole('button', { name: 'Contact administrator' })
  ).toBeEnabled()
})

it.each<[string, string, RegExp, RegExp, RegExp]>([
  [
    'Monthly',
    '101',
    /renew/i,
    /extend.*(term|time|expiry)|only.*extend/i,
    /not.*(refill|replenish|restore).*quota|quota.*not.*(refill|replenish|restore)/i,
  ],
  [
    'Quarterly',
    '102',
    /replace/i,
    /no.*(time conversion|proration)|without.*(time conversion|proration)/i,
    /no.*(quota rollover|quota carry)|without.*(quota rollover|quota carry)/i,
  ],
])(
  'explains the exact %s SKU as a manual administrator action without a write or checkout',
  async (cycle, id, action, term, quota) => {
    const snapshot = view()
    assert(snapshot.entitlements.subscription && snapshot.entitlements.quota)
    snapshot.entitlements.subscription = {
      ...snapshot.entitlements.subscription,
      plan_id: '101',
    }
    snapshot.entitlements.status = 'exhausted'
    snapshot.entitlements.quota = {
      ...snapshot.entitlements.quota,
      used: '1000',
      remaining: '0',
      used_percentage: 100,
    }
    renderPlans(snapshot)
    await screen.findByRole('article', { name: /Pro/ })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: cycle }))
    await user.click(
      within(screen.getByRole('article', { name: /Pro/ })).getByRole('button', {
        name: 'Contact administrator',
      })
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { level: 3 })).toHaveTextContent(
      new RegExp(`#${id}$`)
    )
    expect(dialog).toHaveTextContent(
      cycle === 'Monthly' ? 'Pro Monthly' : 'Pro Quarterly'
    )
    expect(dialog).toHaveTextContent(
      cycle === 'Monthly' ? /1 month|monthly/i : /3 months|quarterly/i
    )
    expect(dialog).toHaveTextContent(
      cycle === 'Monthly' ? /USD\s*10/ : /USD\s*24/
    )
    expect(dialog).not.toHaveTextContent('CNY')
    expect(dialog).toHaveTextContent(action)
    expect(dialog).toHaveTextContent(term)
    expect(dialog).toHaveTextContent(quota)
    expect(
      within(dialog).queryByRole('button', {
        name: /pay|purchase|checkout|submit|apply/i,
      })
    ).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('link')).not.toBeInTheDocument()
    await user.keyboard('{Escape}')
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    expect(requests.length).toBeGreaterThan(0)
    expect(
      requests.every(
        (request) =>
          request.method === 'GET' && request.url === '/api/xcode/v1/web/view'
      )
    ).toBe(true)
    expect(
      screen.queryByText(
        /payment successful|application submitted|order created/i
      )
    ).not.toBeInTheDocument()
  }
)

it('does not offer administrator CTA for an enabled but unassignable template', async () => {
  const snapshot = view()
  snapshot.plan_presentations = snapshot.plan_presentations.map((item) => ({
    ...item,
    assignable: false,
    assignment_reason: 'template_config',
  }))
  renderPlans(snapshot)
  expect(await screen.findByRole('article', { name: /Pro/ })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Contact administrator' })
  ).not.toBeInTheDocument()
})

it('shows a missing selected cycle without substituting another group monthly SKU or CTA', async () => {
  const snapshot = view()
  snapshot.plans.items = [snapshot.plans.items[0], snapshot.plans.items[1]]
  snapshot.plan_presentations = [
    snapshot.plan_presentations[0],
    {
      ...snapshot.plan_presentations[1],
      metadata: { ...metadata, group_key: 'other', group_title: 'Other' },
    },
  ]
  renderPlans(snapshot)
  const pro = await screen.findByRole('article', { name: /^Pro/ })
  expect(pro).toHaveTextContent(
    /no template.*(cycle|period)|no.*(cycle|period).*template/i
  )
  expect(
    within(pro).queryByRole('button', { name: 'Contact administrator' })
  ).not.toBeInTheDocument()
  expect(pro).not.toHaveTextContent(/USD\s*10/)
})

it('allows choosing an entirely missing yearly cycle without falling back to a monthly template', async () => {
  const snapshot = view()
  snapshot.plans.items = [snapshot.plans.items[0]]
  snapshot.plan_presentations = [snapshot.plan_presentations[0]]
  renderPlans(snapshot)
  await screen.findByRole('article', { name: /^Pro/ })
  await userEvent.setup().click(screen.getByRole('button', { name: 'Yearly' }))
  const pro = screen.getByRole('article', { name: /^Pro/ })
  expect(pro).toHaveTextContent(
    /no template.*(cycle|period)|no.*(cycle|period).*template/i
  )
  expect(
    within(pro).queryByRole('button', { name: 'Contact administrator' })
  ).not.toBeInTheDocument()
  expect(pro).not.toHaveTextContent(/USD\s*10/)
})
