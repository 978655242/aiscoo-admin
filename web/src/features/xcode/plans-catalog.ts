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
import type { XCodeWebPlan, XCodeWebView } from './plans-api'

// XCODE: the catalog derives display only; each card keeps its native SKU and grant eligibility.
export type PlanCycle = 'monthly' | 'quarterly' | 'yearly'
export type PlanPresentation = XCodeWebView['plan_presentations'][number]
export type CatalogPlan = {
  plan: XCodeWebPlan
  presentation: PlanPresentation
  issue: 'metadata_conflict' | 'invalid_display' | null
}
export type PlanCatalog = {
  cycles: PlanCycle[]
  defaultCycle: PlanCycle | null
  groups: {
    groupKey: string
    groupTitle: string
    currency: string
    slots: Record<PlanCycle, CatalogPlan[]>
  }[]
  independent: CatalogPlan[]
}
export type PlanPrice = {
  currency: string
  totalAmount: string
  monthlyAmount: string | null
  savingPercent: string | null
}

export function calendarMonths(plan: XCodeWebPlan): 1 | 3 | 12 | null {
  if (plan.duration_unit === 'year' && plan.duration_value === '1') return 12
  if (plan.duration_unit !== 'month') return null
  if (plan.duration_value === '1') return 1
  if (plan.duration_value === '3') return 3
  if (plan.duration_value === '12') return 12
  return null
}

function sameMetadata(
  left: PlanPresentation['metadata'],
  right: PlanPresentation['metadata']
): boolean {
  if (!left || !right) return false
  return (
    left.group_key === right.group_key &&
    left.group_title === right.group_title &&
    left.offer_kind === right.offer_kind &&
    JSON.stringify(left.highlights) === JSON.stringify(right.highlights) &&
    JSON.stringify(left.features) === JSON.stringify(right.features)
  )
}

export function buildPlanCatalog(
  plans: XCodeWebPlan[],
  presentations: PlanPresentation[]
): PlanCatalog {
  const catalog: PlanCatalog = {
    cycles: [],
    defaultCycle: null,
    groups: [],
    independent: [],
  }
  const byId = new Map(presentations.map((item) => [item.plan_id, item]))
  const buckets = new Map<
    string,
    {
      metadata: NonNullable<PlanPresentation['metadata']>
      cards: CatalogPlan[]
    }
  >()
  for (const plan of plans) {
    const presentation = byId.get(plan.id) ?? {
      plan_id: plan.id,
      metadata: null,
      assignable: false,
      assignment_reason: 'template_config' as const,
      display_issue: 'invalid_display' as const,
    }
    const card: CatalogPlan = {
      plan,
      presentation,
      issue: presentation.display_issue,
    }
    if (!presentation.metadata || presentation.display_issue) {
      catalog.independent.push(card)
      continue
    }
    const key = JSON.stringify([presentation.metadata.group_key, plan.currency])
    const bucket = buckets.get(key)
    if (bucket) bucket.cards.push(card)
    else buckets.set(key, { metadata: presentation.metadata, cards: [card] })
  }
  for (const { metadata, cards } of buckets.values()) {
    if (
      cards.some((card) => !sameMetadata(metadata, card.presentation.metadata))
    ) {
      catalog.independent.push(
        ...cards.map((card) => ({
          ...card,
          issue: 'metadata_conflict' as const,
        }))
      )
      continue
    }
    const group: PlanCatalog['groups'][number] = {
      groupKey: metadata.group_key,
      groupTitle: metadata.group_title,
      currency: cards[0].plan.currency,
      slots: { monthly: [], quarterly: [], yearly: [] },
    }
    for (const card of cards) {
      const months = calendarMonths(card.plan)
      if (!months || metadata.offer_kind === 'trial') {
        catalog.independent.push(card)
        continue
      }
      let cycle: PlanCycle = 'monthly'
      if (months === 3) cycle = 'quarterly'
      if (months === 12) cycle = 'yearly'
      group.slots[cycle].push(card)
    }
    if (Object.values(group.slots).some((slot) => slot.length > 0)) {
      catalog.groups.push(group)
    }
  }
  catalog.cycles = (['monthly', 'quarterly', 'yearly'] as const).filter(
    (cycle) => catalog.groups.some((group) => group.slots[cycle].length > 0)
  )
  if (catalog.cycles.includes('quarterly')) catalog.defaultCycle = 'quarterly'
  else catalog.defaultCycle = catalog.cycles[0] ?? null
  return catalog
}

function decimalFraction(amount: string): {
  numerator: bigint
  denominator: bigint
} {
  const [whole, fraction = ''] = amount.split('.')
  return {
    numerator: BigInt(whole + fraction),
    denominator: 10n ** BigInt(fraction.length),
  }
}

function roundedDecimal(numerator: bigint, denominator: bigint): string {
  // XCODE: only final display rounds; all comparison and division stays integer-rational.
  const cents = (numerator * 100n + denominator / 2n) / denominator
  const fraction = (cents % 100n).toString().padStart(2, '0').replace(/0+$/, '')
  return `${cents / 100n}${fraction ? `.${fraction}` : ''}`
}

export function formatPlanPrice(
  card: CatalogPlan,
  monthlyBaseline: CatalogPlan | null
): PlanPrice {
  const plan = card.plan
  const amount = decimalFraction(plan.price_amount)
  const months = calendarMonths(plan)
  const price: PlanPrice = {
    currency: plan.currency,
    totalAmount: plan.price_amount
      .replace(/(\.\d*?)0+$/, '$1')
      .replace(/\.$/, ''),
    monthlyAmount: months
      ? roundedDecimal(amount.numerator, amount.denominator * BigInt(months))
      : null,
    savingPercent: null,
  }
  if (
    !months ||
    !monthlyBaseline ||
    card.issue ||
    monthlyBaseline.issue ||
    amount.numerator === 0n
  ) {
    return price
  }
  const baseline = monthlyBaseline.plan
  if (
    calendarMonths(baseline) !== 1 ||
    baseline.currency !== plan.currency ||
    !sameMetadata(
      card.presentation.metadata,
      monthlyBaseline.presentation.metadata
    ) ||
    plan.total_quota !== baseline.total_quota ||
    plan.reset_period !== baseline.reset_period ||
    plan.reset_custom_seconds !== baseline.reset_custom_seconds
  ) {
    return price
  }
  // XCODE: 自定义间隔可能晚于到期，不能把一期总额当作每月预算。
  // shortcut: 有限 custom 不比较折扣；取得实际可比重置预算依据后启用。
  if (
    plan.total_quota !== '0' &&
    (plan.reset_period === 'never' || plan.reset_period === 'custom')
  ) {
    return price
  }
  const monthly = decimalFraction(baseline.price_amount)
  if (monthly.numerator === 0n) return price
  const reference = monthly.numerator * BigInt(months) * amount.denominator
  const total = amount.numerator * monthly.denominator
  if (total >= reference) return price
  const saving = roundedDecimal((reference - total) * 100n, reference)
  if (saving !== '0' && saving !== '100') price.savingPercent = saving
  return price
}
