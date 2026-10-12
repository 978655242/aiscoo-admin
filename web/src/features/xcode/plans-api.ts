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
import { z } from 'zod'

import {
  xcodeAuthorizationSnapshotSchema,
  xcodePlanDisplaySchema,
} from '@/features/subscriptions/types'

const integer = z.string().regex(/^(0|[1-9]\d*)$/)
const decimal = z.string().regex(/^(0|[1-9]\d*)(\.\d+)?$/)
const positiveDecimal = decimal.refine(
  (value) => Number.isFinite(Number(value)) && Number(value) > 0
)
const timestamp = z.number().int().safe().nonnegative().max(8640000000000000)
const resetPeriod = z.enum(['never', 'daily', 'weekly', 'monthly', 'custom'])

const planPresentationSchema = z
  .object({
    plan_id: integer,
    metadata: xcodePlanDisplaySchema.nullable(),
    assignable: z.boolean(),
    assignment_reason: z.enum(['template_config']).nullable(),
    display_issue: z.enum(['invalid_display']).nullable(),
  })
  .strict()
export const xcodeWebViewSchema = z
  .object({
    schema_version: z.literal(1),
    server_time: timestamp,
    plans: z.object({
      schema_version: z.literal(1),
      server_time: timestamp,
      purchase_mode: z.literal('manual_admin'),
      self_service_purchase: z.literal(false),
      items: z.array(
        z.object({
          id: integer,
          title: z.string(),
          subtitle: z.string(),
          price_amount: decimal,
          currency: z.string().trim().min(1),
          duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
          duration_value: integer,
          duration_custom_seconds: integer,
          total_quota: integer,
          reset_period: resetPeriod,
          reset_custom_seconds: integer,
          enabled: z.literal(true),
        })
      ),
    }),
    entitlements: xcodeAuthorizationSnapshotSchema.refine((value) => {
      if (value.status === 'unassigned') {
        return value.subscription === null && value.quota === null
      }
      if (!value.subscription || !value.quota) return false
      const dates = [
        value.server_time,
        value.subscription.start_time,
        value.subscription.end_time,
        value.quota.last_reset_time,
        value.quota.next_reset_time,
      ]
      if (dates.some((date) => date !== null && date > 8640000000000000)) {
        return false
      }
      const quota = value.quota
      if (quota.unlimited) {
        return (
          quota.total === null &&
          quota.remaining === null &&
          quota.used_percentage === null
        )
      }
      return (
        quota.total !== null &&
        quota.total !== '0' &&
        quota.remaining !== null &&
        quota.used_percentage !== null &&
        quota.used_percentage <= 100
      )
    }),
    // XCODE: only the Web view accepts display metadata; public plan items stay strict and unchanged.
    plan_presentations: z.array(planPresentationSchema),
    display: z
      .object({
        quota_per_unit: positiveDecimal,
        display_type: z.enum(['USD', 'CNY', 'CUSTOM', 'TOKENS']),
        currency: z.string().nullable(),
        currency_symbol: z.string(),
        usd_to_currency_rate: positiveDecimal.nullable(),
      })
      .refine((value) => {
        if (value.display_type === 'TOKENS') {
          return value.currency === null && value.usd_to_currency_rate === null
        }
        if (value.usd_to_currency_rate === null) return false
        if (value.display_type === 'CUSTOM') return value.currency === null
        return value.currency === value.display_type
      }),
  })
  .superRefine((view, context) => {
    const ids = new Set(view.plans.items.map((plan) => plan.id))
    const seen = new Set<string>()
    // XCODE: 拒绝错配/重复 sidecar，不能把另一模板的展示或授予资格绑定到当前 SKU。
    if (
      ids.size !== view.plans.items.length ||
      ids.size !== view.plan_presentations.length
    ) {
      context.addIssue({
        code: 'custom',
        path: ['plan_presentations'],
        message: 'Invalid plan presentation coverage',
      })
    }
    view.plan_presentations.forEach((item, index) => {
      if (
        !ids.has(item.plan_id) ||
        seen.has(item.plan_id) ||
        (item.assignable
          ? item.assignment_reason !== null
          : item.assignment_reason !== 'template_config') ||
        (item.display_issue !== null &&
          (item.metadata !== null || item.assignable))
      ) {
        context.addIssue({
          code: 'custom',
          path: ['plan_presentations', index],
          message: 'Invalid exact-ID plan presentation',
        })
      }
      seen.add(item.plan_id)
    })
  })

export type XCodeWebView = z.infer<typeof xcodeWebViewSchema>
export type XCodeWebPlan = XCodeWebView['plans']['items'][number]
export type XCodeWebViewFailure = 'invalid' | 'expired' | 'unavailable'

export class XCodeWebViewError extends Error {
  constructor(readonly reason: XCodeWebViewFailure) {
    super('XCode read-only view unavailable')
    this.name = 'XCodeWebViewError'
  }
}

export async function fetchXCodeWebView(
  signal: AbortSignal
): Promise<XCodeWebView> {
  const response = await fetch('/api/xcode/v1/web/view', {
    method: 'GET',
    credentials: 'same-origin',
    cache: 'no-store',
    referrerPolicy: 'no-referrer',
    signal,
  })
  if (response.status === 400) throw new XCodeWebViewError('invalid')
  if (response.status === 401 || response.status === 403) {
    throw new XCodeWebViewError('expired')
  }
  if (!response.ok) throw new XCodeWebViewError('unavailable')
  let body: unknown
  try {
    body = await response.json()
  } catch {
    throw new XCodeWebViewError('invalid')
  }
  const parsed = z
    .object({
      success: z.literal(true),
      data: xcodeWebViewSchema,
    })
    .safeParse(body)
  if (!parsed.success) throw new XCodeWebViewError('invalid')
  return parsed.data.data
}
