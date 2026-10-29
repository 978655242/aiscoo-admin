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

// ============================================================================
// Subscription Plan Schema & Types
// ============================================================================

function displayTextValid(value: string, maxCodePoints: number): boolean {
  if (value.trim() === '') return false
  let count = 0
  // XCODE: Go 按 Unicode code point 校验；UTF-16 length 会把合法 emoji 边界拒绝为整页无效。
  for (let index = 0; index < value.length; index++) {
    if (++count > maxCodePoints) return false
    const codePoint = value.codePointAt(index)
    if (codePoint !== undefined && codePoint > 0xffff) index++
  }
  return true
}

// XCODE: display metadata is presentation-only and remains separate from plan economics.
export const xcodePlanDisplaySchema = z
  .object({
    group_key: z.string().regex(/^[A-Za-z0-9_-]{1,64}$/),
    group_title: z.string().refine((value) => displayTextValid(value, 128)),
    offer_kind: z.enum(['standard', 'trial']),
    highlights: z
      .array(z.string().refine((value) => displayTextValid(value, 256)))
      .max(4),
    features: z
      .array(z.string().refine((value) => displayTextValid(value, 256)))
      .max(16),
  })
  .strict()

export type XCodePlanDisplay = z.infer<typeof xcodePlanDisplaySchema>

export const subscriptionPlanSchema = z.object({
  id: z.number(),
  title: z.string(),
  subtitle: z.string().optional(),
  price_amount: z.number(),
  currency: z.string().default('USD'),
  duration_unit: z.enum(['year', 'month', 'day', 'hour', 'custom']),
  duration_value: z.number(),
  custom_seconds: z.number().optional(),
  quota_reset_period: z.enum(['never', 'daily', 'weekly', 'monthly', 'custom']),
  quota_reset_custom_seconds: z.number().optional(),
  enabled: z.boolean(),
  sort_order: z.number(),
  allow_balance_pay: z.boolean().optional().default(true),
  allow_wallet_overflow: z.boolean().optional().default(true),
  max_purchase_per_user: z.number(),
  total_amount: z.number(),
  upgrade_group: z.string().optional(),
  downgrade_group: z.string().optional(),
  stripe_price_id: z.string().optional(),
  creem_product_id: z.string().optional(),
  waffo_pancake_product_id: z.string().optional(),
  xcode_display: xcodePlanDisplaySchema.nullable().optional(),
})

export type SubscriptionPlan = z.infer<typeof subscriptionPlanSchema>

export interface PlanRecord {
  plan: SubscriptionPlan
}

// ============================================================================
// User Subscription Schema & Types
// ============================================================================

export const userSubscriptionSchema = z.object({
  id: z.number(),
  user_id: z.number(),
  plan_id: z.number(),
  status: z.string(),
  source: z.string().optional(),
  usage_scope: z.string().optional(),
  start_time: z.number(),
  end_time: z.number(),
  amount_total: z.number(),
  amount_used: z.number(),
  next_reset_time: z.number().optional(),
})

export type UserSubscription = z.infer<typeof userSubscriptionSchema>

export interface UserSubscriptionRecord {
  subscription: UserSubscription
}

// ============================================================================
// API Request/Response Types
// ============================================================================

export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface PlanPayload {
  plan: Partial<SubscriptionPlan>
}

export interface SubscriptionPayRequest {
  plan_id: number
  payment_method?: string
}

export interface SubscriptionPayResponse {
  success: boolean
  message?: string
  data?: {
    // Stripe-style hosted checkout link.
    pay_link?: string
    // Waffo Pancake / Creem hosted checkout URL.
    checkout_url?: string
    // Pancake-only: order metadata + self-service buyer session token,
    // surfaced for future flows (refund / cancel from aiscoo's own UI).
    session_id?: string
    expires_at?: number | string
    order_id?: string
    token?: string
    token_expires_at?: number | string
  }
  url?: string
}

export interface CreateUserSubscriptionRequest {
  plan_id: number
}

export interface ResetUserSubscriptionsRequest {
  plan_id: number
  advance_reset_time: boolean
}

export interface ResetPlanSubscriptionsRequest {
  advance_reset_time: boolean
}

export interface SubscriptionResetResult {
  plan_id: number
  matched_count: number
  reset_count: number
  user_count: number
  advance_reset_time: boolean
}

const xcodeDecimalSchema = z.string().regex(/^(0|[1-9]\d*)$/)
const xcodeTimestampSchema = z.number().int().safe().nonnegative().nullable()

export const xcodeAuthorizationSnapshotSchema = z.object({
  schema_version: z.literal(1),
  server_time: z.number().int().safe().nonnegative(),
  status: z.enum(['unassigned', 'active', 'exhausted', 'expired', 'revoked']),
  authorization_revision: xcodeDecimalSchema,
  subscription: z
    .object({
      id: xcodeDecimalSchema,
      plan_id: xcodeDecimalSchema,
      plan_title: z.string(),
      source: z.literal('admin'),
      usage_scope: z.literal('xcode'),
      start_time: xcodeTimestampSchema,
      end_time: xcodeTimestampSchema,
    })
    .nullable(),
  quota: z
    .object({
      scope: z.literal('xcode-subscription'),
      unit: z.literal('quota'),
      unlimited: z.boolean(),
      total: xcodeDecimalSchema.nullable(),
      used: xcodeDecimalSchema,
      remaining: xcodeDecimalSchema.nullable(),
      used_percentage: z.number().finite().nonnegative().nullable(),
      reset_period: z.enum(['never', 'daily', 'weekly', 'monthly', 'custom']),
      reset_custom_seconds: xcodeDecimalSchema,
      last_reset_time: xcodeTimestampSchema,
      next_reset_time: xcodeTimestampSchema,
      reset_time_zone: z.string(),
      reset_utc_offset_seconds: z.number().int(),
    })
    .nullable(),
  funding_policy: z.object({
    subscription_only: z.literal(true),
    wallet_fallback: z.literal(false),
    other_subscription_fallback: z.literal(false),
  }),
  self_service_reset: z.literal(false),
  purchase_mode: z.literal('manual_admin'),
})

export type XCodeAuthorizationSnapshot = z.infer<
  typeof xcodeAuthorizationSnapshotSchema
>

export type XCodeAuthorizationRequest =
  | {
      action: 'grant' | 'renew' | 'replace'
      expected_revision: string
      plan_id: string
    }
  | { action: 'revoke'; expected_revision: string }
  | {
      action: 'reset'
      expected_revision: string
      advance_reset_time: boolean
    }

// ============================================================================
// Self Subscription Data (user-facing)
// ============================================================================

export interface SelfSubscriptionData {
  billing_preference: string
  subscriptions: UserSubscriptionRecord[]
  all_subscriptions: UserSubscriptionRecord[]
}

// ============================================================================
// Dialog Types
// ============================================================================

export type SubscriptionsDialogType =
  | 'create'
  | 'update'
  | 'toggle-status'
  | 'reset-subscriptions'
