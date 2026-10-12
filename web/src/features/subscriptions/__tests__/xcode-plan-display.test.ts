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
import { expect, test } from 'vitest'

import { xcodeWebViewSchema } from '@/features/xcode/plans-api'

import { formValuesToPlanPayload, planToFormValues } from '../lib'
import { subscriptionPlanSchema } from '../types'

const metadata = {
  group_key: 'pro-tier',
  group_title: 'Pro',
  offer_kind: 'standard' as const,
  highlights: ['Priority models'],
  features: ['Long context', 'Admin grant'],
}

const nativePlan = {
  id: 7,
  title: 'Quarterly Pro',
  subtitle: 'Administrator assigned',
  price_amount: 24,
  currency: 'USD',
  duration_unit: 'month' as const,
  duration_value: 3,
  custom_seconds: 0,
  quota_reset_period: 'monthly' as const,
  quota_reset_custom_seconds: 0,
  enabled: true,
  sort_order: 10,
  allow_balance_pay: true,
  allow_wallet_overflow: true,
  max_purchase_per_user: 0,
  total_amount: 1_500_000,
  upgrade_group: '',
  downgrade_group: '',
  stripe_price_id: '',
  creem_product_id: '',
  waffo_pancake_product_id: '',
}

test('subscription plan parsing and form conversion preserve display metadata and explicit null clearing', () => {
  const configured = subscriptionPlanSchema.parse({
    ...nativePlan,
    xcode_display: metadata,
  })
  expect(configured.xcode_display).toEqual(metadata)

  const configuredForm = planToFormValues(configured)
  expect(configuredForm.xcode_display).toEqual(metadata)
  expect(formValuesToPlanPayload(configuredForm).plan.xcode_display).toEqual(
    metadata
  )

  const cleared = subscriptionPlanSchema.parse({
    ...nativePlan,
    xcode_display: null,
  })
  expect(cleared.xcode_display).toBeNull()

  const clearedForm = planToFormValues(cleared)
  expect(clearedForm.xcode_display).toBeNull()
  expect(formValuesToPlanPayload(clearedForm).plan.xcode_display).toBeNull()
})

test('form metadata uses the same Unicode code-point boundaries as native CRUD', () => {
  const boundary = {
    ...metadata,
    group_title: `${'a'.repeat(127)}😀`,
    highlights: [`${'a'.repeat(255)}😀`],
    features: [`${'a'.repeat(255)}😀`],
  }
  const parsed = subscriptionPlanSchema.parse({
    ...nativePlan,
    xcode_display: boundary,
  })
  expect(
    formValuesToPlanPayload(planToFormValues(parsed)).plan.xcode_display
  ).toEqual(boundary)
  for (const overflow of [
    { group_title: `${boundary.group_title}x` },
    { highlights: [`${boundary.highlights[0]}x`] },
    { features: [`${boundary.features[0]}x`] },
  ]) {
    expect(
      subscriptionPlanSchema.safeParse({
        ...nativePlan,
        xcode_display: { ...boundary, ...overflow },
      }).success
    ).toBe(false)
  }
})

test('subscription and web schemas reject invalid display metadata while accepting null', () => {
  expect(
    subscriptionPlanSchema.safeParse({
      ...nativePlan,
      xcode_display: { ...metadata, group_key: 'invalid key' },
    }).success
  ).toBe(false)
  expect(
    subscriptionPlanSchema.safeParse({
      ...nativePlan,
      xcode_display: { ...metadata, offer_kind: 'discount' },
    }).success
  ).toBe(false)
  expect(
    subscriptionPlanSchema.safeParse({ ...nativePlan, xcode_display: null })
      .success
  ).toBe(true)
})

test('web view parsing preserves the exact-ID presentation sidecar', () => {
  const presentation = {
    plan_id: '9007199254740993',
    metadata: {
      ...metadata,
      group_title: `${'a'.repeat(127)}😀`,
      highlights: [`${'a'.repeat(255)}😀`],
      features: [`${'a'.repeat(255)}😀`],
    },
    assignable: true,
    assignment_reason: null,
    display_issue: null,
  }
  const input = {
    schema_version: 1,
    server_time: 1_791_400_000_000,
    plans: {
      schema_version: 1,
      server_time: 1_791_400_000_000,
      purchase_mode: 'manual_admin',
      self_service_purchase: false,
      items: [
        {
          id: '9007199254740993',
          title: 'Quarterly Pro',
          subtitle: 'Administrator assigned',
          price_amount: '24',
          currency: 'USD',
          duration_unit: 'month',
          duration_value: '3',
          duration_custom_seconds: '0',
          total_quota: '1500000',
          reset_period: 'monthly',
          reset_custom_seconds: '0',
          enabled: true,
        },
      ],
    },
    entitlements: {
      schema_version: 1,
      server_time: 1_791_400_000_000,
      status: 'unassigned',
      authorization_revision: '0',
      subscription: null,
      quota: null,
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
      display_type: 'USD',
      currency: 'USD',
      currency_symbol: '$',
      usd_to_currency_rate: '1',
    },
    plan_presentations: [presentation],
  }
  const view = xcodeWebViewSchema.parse(input)

  expect(view.plan_presentations).toEqual([presentation])

  expect(
    xcodeWebViewSchema.safeParse({
      ...input,
      plan_presentations: [
        {
          ...presentation,
          metadata: { ...metadata, group_key: 'invalid key' },
        },
      ],
    }).success
  ).toBe(false)
  expect(
    xcodeWebViewSchema.safeParse({
      ...input,
      plan_presentations: [
        { ...presentation, metadata: { ...metadata, offer_kind: 'discount' } },
      ],
    }).success
  ).toBe(false)
  const withoutMetadata = xcodeWebViewSchema.parse({
    ...input,
    plan_presentations: [{ ...presentation, metadata: null }],
  })
  expect(withoutMetadata.plan_presentations[0]?.metadata).toBeNull()
  for (const plan_presentations of [
    [],
    [presentation, presentation],
    [{ ...presentation, plan_id: '7' }],
    [
      {
        ...presentation,
        assignable: true,
        assignment_reason: 'template_config',
      },
    ],
    [{ ...presentation, assignable: false, assignment_reason: null }],
    [{ ...presentation, display_issue: 'invalid_display' }],
  ]) {
    expect(
      xcodeWebViewSchema.safeParse({ ...input, plan_presentations }).success
    ).toBe(false)
  }
})
