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
import { describe, expect, it } from 'vitest'

import type { XCodeWebPlan, XCodeWebView } from '../plans-api'
import { buildPlanCatalog, formatPlanPrice } from '../plans-catalog'

// XCODE: RED-stage consumer contract; production exports may use these structural types.
type Cycle = 'monthly' | 'quarterly' | 'yearly'
type Presentation = XCodeWebView['plan_presentations'][number]
type CatalogPlan = {
  plan: XCodeWebPlan
  presentation: Presentation
  issue: 'metadata_conflict' | 'invalid_display' | null
}
type Catalog = {
  cycles: Cycle[]
  defaultCycle: Cycle | null
  groups: {
    groupKey: string
    groupTitle: string
    currency: string
    slots: Record<Cycle, CatalogPlan[]>
  }[]
  independent: CatalogPlan[]
}
type PlanPrice = {
  currency: string
  totalAmount: string
  monthlyAmount: string | null
  savingPercent: string | null
}

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
    title: `Template ${id}`,
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

function presentation(
  id: string,
  overrides: Partial<Presentation> = {}
): Presentation {
  return {
    plan_id: id,
    metadata,
    assignable: true,
    assignment_reason: null,
    display_issue: null,
    ...overrides,
  }
}

function priced(
  overrides: Partial<XCodeWebPlan> = {},
  display = metadata
): CatalogPlan {
  return {
    plan: plan('101', overrides),
    presentation: presentation('101', { metadata: display }),
    issue: null,
  }
}

const monthly = plan('101')
const quarterly = plan('102', { duration_value: '3', price_amount: '24' })
const duplicateMonthly = plan('103', { price_amount: '9' })
const annual = plan('104', { duration_unit: 'year', price_amount: '96' })

function ids(cards: CatalogPlan[]): string[] {
  return cards.map((card) => card.plan.id).sort()
}

describe('buildPlanCatalog', () => {
  it.each<[XCodeWebPlan, XCodeWebPlan, XCodeWebPlan]>([
    [monthly, quarterly, duplicateMonthly],
    [monthly, duplicateMonthly, quarterly],
    [quarterly, monthly, duplicateMonthly],
    [quarterly, duplicateMonthly, monthly],
    [duplicateMonthly, monthly, quarterly],
    [duplicateMonthly, quarterly, monthly],
  ])('preserves duplicate monthly IDs for input order %j', (...plans) => {
    const catalog: Catalog = buildPlanCatalog(plans, [
      presentation('103'),
      presentation('102'),
      presentation('101'),
    ])
    expect(catalog.defaultCycle).toBe('quarterly')
    expect(catalog.cycles).toEqual(['monthly', 'quarterly'])
    expect(catalog.groups).toHaveLength(1)
    expect(ids(catalog.groups[0].slots.monthly)).toEqual(['101', '103'])
    expect(ids(catalog.groups[0].slots.quarterly)).toEqual(['102'])
    expect(catalog.groups[0].slots.yearly).toEqual([])
    expect(catalog.independent).toEqual([])
    expect(
      catalog.groups[0].slots.monthly.map((card) => card.plan.title).sort()
    ).toEqual(['Template 101', 'Template 103'])
  })

  it('treats month/12 and year/1 as a collision without damaging monthly or quarterly slots', () => {
    const plans = [
      monthly,
      quarterly,
      annual,
      plan('105', { duration_value: '12' }),
    ]
    const catalog: Catalog = buildPlanCatalog(
      plans,
      plans.map((item) => presentation(item.id))
    )
    expect(ids(catalog.groups[0].slots.yearly)).toEqual(['104', '105'])
    expect(ids(catalog.groups[0].slots.monthly)).toEqual(['101'])
    expect(ids(catalog.groups[0].slots.quarterly)).toEqual(['102'])
  })

  it('leaves a missing cycle empty instead of substituting a monthly SKU', () => {
    const catalog: Catalog = buildPlanCatalog([monthly], [presentation('101')])
    expect(catalog.defaultCycle).toBe('monthly')
    expect(catalog.cycles).toEqual(['monthly'])
    expect(catalog.groups[0].slots.quarterly).toEqual([])
    expect(catalog.groups[0].slots.yearly).toEqual([])
  })

  it('keeps currencies in distinct groups even with the same group key', () => {
    const eur = plan('106', { currency: 'EUR', duration_value: '3' })
    const catalog: Catalog = buildPlanCatalog(
      [monthly, eur],
      [presentation('101'), presentation('106')]
    )
    expect(catalog.groups).toHaveLength(2)
    expect(catalog.groups.map((group) => group.currency).sort()).toEqual([
      'EUR',
      'USD',
    ])
    const usd = catalog.groups.find((group) => group.currency === 'USD')
    const euros = catalog.groups.find((group) => group.currency === 'EUR')
    if (!usd || !euros) throw new Error('Expected distinct USD and EUR groups')
    expect(ids(usd.slots.monthly)).toEqual(['101'])
    expect(usd.slots.quarterly).toEqual([])
    expect(ids(euros.slots.quarterly)).toEqual(['106'])
    expect(euros.slots.monthly).toEqual([])
  })

  it.each([
    { group_title: 'Different title' },
    { offer_kind: 'trial' as const },
    { highlights: ['Different highlight'] },
    { features: ['Different entitlement'] },
  ])(
    'falls back the entire group to exact-ID cards on metadata conflict %j',
    (conflict) => {
      const catalog: Catalog = buildPlanCatalog(
        [monthly, quarterly],
        [
          presentation('101'),
          presentation('102', { metadata: { ...metadata, ...conflict } }),
        ]
      )
      expect(catalog.groups).toEqual([])
      expect(ids(catalog.independent)).toEqual(['101', '102'])
      expect(
        catalog.independent.every((card) => card.issue === 'metadata_conflict')
      ).toBe(true)
    }
  )

  it('preserves unconfigured, damaged, day90, hour, custom and other-month templates as independent cards', () => {
    const plans = [
      monthly,
      plan('107', { duration_unit: 'day', duration_value: '90' }),
      plan('108', {
        duration_unit: 'custom',
        duration_custom_seconds: '7776000',
      }),
      plan('109', { duration_value: '2' }),
      plan('110', { duration_unit: 'hour', duration_value: '24' }),
      plan('111'),
      plan('112'),
    ]
    const sidecars = plans.map((item) => presentation(item.id))
    sidecars[0] = presentation('101', { metadata: null })
    sidecars[5] = presentation('111', {
      metadata: null,
      display_issue: 'invalid_display',
      assignable: false,
      assignment_reason: 'template_config',
    })
    sidecars[6] = presentation('112', {
      assignable: false,
      assignment_reason: 'template_config',
    })
    const catalog: Catalog = buildPlanCatalog(plans, sidecars)
    expect(ids(catalog.independent)).toEqual([
      '101',
      '107',
      '108',
      '109',
      '110',
      '111',
    ])
    expect(
      catalog.independent.find((card) => card.plan.id === '111')?.issue
    ).toBe('invalid_display')
    expect(
      catalog.independent.find((card) => card.plan.id === '108')?.plan
        .duration_custom_seconds
    ).toBe('7776000')
    expect(catalog.groups[0].slots.monthly[0].presentation.assignable).toBe(
      false
    )
    expect(catalog.groups[0].slots.quarterly).toEqual([])
  })

  it('does not invent a calendar selection when only independent templates exist', () => {
    const catalog: Catalog = buildPlanCatalog(
      [plan('107', { duration_unit: 'day', duration_value: '90' })],
      [presentation('107')]
    )
    expect(catalog.cycles).toEqual([])
    expect(catalog.defaultCycle).toBeNull()
    expect(ids(catalog.independent)).toEqual(['107'])
  })

  it('preserves every explicit trial independently and never treats a free standard SKU as trial', () => {
    const plans = [
      plan('101', { price_amount: '0' }),
      plan('201', { duration_unit: 'day', duration_value: '7' }),
      plan('202', { duration_unit: 'day', duration_value: '14' }),
    ]
    const trial = {
      ...metadata,
      group_key: 'trial',
      group_title: 'Trial',
      offer_kind: 'trial' as const,
    }
    const catalog: Catalog = buildPlanCatalog(plans, [
      presentation('101'),
      presentation('201', { metadata: trial }),
      presentation('202', { metadata: trial }),
    ])
    expect(
      catalog.groups[0].slots.monthly[0].presentation.metadata?.offer_kind
    ).toBe('standard')
    expect(ids(catalog.independent)).toEqual(['201', '202'])
  })
})

describe('formatPlanPrice', () => {
  it.each<[Partial<XCodeWebPlan>, string]>([
    [{ duration_value: '3', price_amount: '24' }, '24'],
    [{ duration_value: '12', price_amount: '96' }, '96'],
    [
      {
        duration_unit: 'year' as const,
        duration_value: '1',
        price_amount: '96',
      },
      '96',
    ],
  ])(
    'shows the native total and exact 8/month with a 20 percent saving for %j',
    (overrides, total) => {
      const price: PlanPrice = formatPlanPrice(priced(overrides), priced())
      expect(price).toEqual({
        currency: 'USD',
        totalAmount: total,
        monthlyAmount: '8',
        savingPercent: '20',
      })
    }
  )

  it('shows the native monthly reference price without inventing a saving', () => {
    const price: PlanPrice = formatPlanPrice(priced(), null)
    expect(price).toEqual({
      currency: 'USD',
      totalAmount: '10',
      monthlyAmount: '10',
      savingPercent: null,
    })
  })

  it('preserves decimal precision beyond Number safe integers', () => {
    const price: PlanPrice = formatPlanPrice(
      priced({ price_amount: '27021597764222979.03', duration_value: '3' }),
      null
    )
    expect(price).toEqual({
      currency: 'USD',
      totalAmount: '27021597764222979.03',
      monthlyAmount: '9007199254740993.01',
      savingPercent: null,
    })
  })

  it('calculates savings from a non-integer decimal baseline without prematurely rounding it', () => {
    const price: PlanPrice = formatPlanPrice(
      priced({ price_amount: '27021597764222979', duration_value: '3' }),
      priced({ price_amount: '10007999171934436.666666666666666667' })
    )
    expect(price.savingPercent).toBe('10')
  })

  it.each<
    [
      string,
      Partial<XCodeWebPlan>,
      Partial<XCodeWebPlan>,
      NonNullable<Presentation['metadata']>,
    ]
  >([
    ['free period', { price_amount: '0' }, {}, metadata],
    ['free baseline', {}, { price_amount: '0' }, metadata],
    ['surcharge', { price_amount: '36' }, {}, metadata],
    ['equal total', { price_amount: '30' }, {}, metadata],
    ['different currency', { currency: 'EUR' }, {}, metadata],
    [
      'finite never-reset budget',
      { reset_period: 'never' as const },
      { reset_period: 'never' as const },
      metadata,
    ],
    ['different quota', { total_quota: '3000' }, {}, metadata],
    ['different reset', { reset_period: 'daily' as const }, {}, metadata],
    [
      'different custom reset',
      { reset_period: 'custom' as const, reset_custom_seconds: '100' },
      { reset_period: 'custom' as const, reset_custom_seconds: '200' },
      metadata,
    ],
    [
      'different features',
      {},
      {},
      { ...metadata, features: ['Different budget entitlement'] },
    ],
    [
      'different highlights',
      {},
      {},
      { ...metadata, highlights: ['Different benefit'] },
    ],
    ['different group', {}, {}, { ...metadata, group_key: 'other' }],
  ])(
    'does not advertise a discount for %s',
    (_name, period, baseline, display) => {
      const price: PlanPrice = formatPlanPrice(
        priced({ duration_value: '3', price_amount: '24', ...period }, display),
        priced(baseline)
      )
      expect(price.savingPercent).toBeNull()
    }
  )

  it('keeps true total and monthly prices without claiming savings for a custom reset later than expiry', () => {
    const reset = {
      reset_period: 'custom' as const,
      reset_custom_seconds: '31536000',
    }
    const price = formatPlanPrice(
      priced({ duration_value: '3', price_amount: '24', ...reset }),
      priced(reset)
    )
    expect(price).toEqual({
      currency: 'USD',
      totalAmount: '24',
      monthlyAmount: '8',
      savingPercent: null,
    })
  })

  it('requires a baseline and does not convert 90 days into three calendar months', () => {
    expect(
      formatPlanPrice(priced({ duration_value: '3', price_amount: '24' }), null)
        .savingPercent
    ).toBeNull()
    const price: PlanPrice = formatPlanPrice(
      priced({
        duration_unit: 'day',
        duration_value: '90',
        price_amount: '24',
      }),
      priced()
    )
    expect(price).toEqual({
      currency: 'USD',
      totalAmount: '24',
      monthlyAmount: null,
      savingPercent: null,
    })
  })

  it('compares unlimited plans without pretending a finite never-reset grant is a monthly budget', () => {
    const price: PlanPrice = formatPlanPrice(
      priced({
        duration_value: '3',
        price_amount: '24',
        total_quota: '0',
        reset_period: 'never',
      }),
      priced({ total_quota: '0', reset_period: 'never' })
    )
    expect(price.savingPercent).toBe('20')
  })
})
