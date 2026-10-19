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
import type { TFunction } from 'i18next'

import { formatResetPeriod } from '@/features/subscriptions/lib/format'
import { formatCurrencyFromUSD, formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import type { CurrencyConfig } from '@/stores/system-config-store'

import type { XCodeWebPlan, XCodeWebView } from './plans-api'

export function formatXCodeQuota(
  value: string | null,
  display: XCodeWebView['display'],
  t: TFunction,
  locale: string | undefined
): string {
  if (value === null) return t('Unlimited')
  if (BigInt(value) > BigInt(Number.MAX_SAFE_INTEGER)) {
    return t('{{value}} quota', { value })
  }
  if (display.display_type === 'TOKENS') {
    return t('{{value}} quota', { value: formatNumber(Number(value), locale) })
  }
  const config: CurrencyConfig = {
    displayInCurrency: true,
    quotaDisplayType: display.display_type,
    quotaPerUnit: Number(display.quota_per_unit),
    usdExchangeRate: Number(display.usd_to_currency_rate),
    customCurrencySymbol: display.currency_symbol,
    customCurrencyExchangeRate: Number(display.usd_to_currency_rate),
  }
  const converted =
    (Number(value) / config.quotaPerUnit) * Number(display.usd_to_currency_rate)
  if (
    !Number.isFinite(converted) ||
    converted > Number.MAX_SAFE_INTEGER ||
    (value !== '0' && converted < 0.0001)
  ) {
    return t('{{value}} quota', { value })
  }
  return formatQuotaWithCurrency(
    Number(value),
    {
      locale,
      abbreviate: false,
    },
    config
  )
}

export function formatXCodeReferencePrice(
  plan: XCodeWebPlan,
  locale: string | undefined
): string {
  const digits = plan.price_amount.split('.')[1]?.length ?? 0
  const significantDigits = plan.price_amount
    .replace('.', '')
    .replace(/^0+/, '').length
  if (
    digits > 15 ||
    significantDigits > 15 ||
    Number(plan.price_amount) > Number.MAX_SAFE_INTEGER
  ) {
    return `${plan.currency} ${plan.price_amount}`
  }
  return formatCurrencyFromUSD(
    Number(plan.price_amount),
    {
      locale,
      digitsLarge: digits,
      digitsSmall: digits,
      abbreviate: false,
    },
    {
      displayInCurrency: true,
      quotaDisplayType: 'CUSTOM',
      quotaPerUnit: 1,
      usdExchangeRate: 1,
      customCurrencySymbol: plan.currency,
      customCurrencyExchangeRate: 1,
    }
  )
}

export function formatXCodeResetPeriod(
  period: XCodeWebPlan['reset_period'],
  seconds: string,
  t: TFunction,
  locale: string | undefined
): string {
  if (period !== 'custom') {
    return formatResetPeriod({ quota_reset_period: period }, t)
  }
  const count =
    BigInt(seconds) > BigInt(Number.MAX_SAFE_INTEGER)
      ? seconds
      : formatNumber(Number(seconds), locale)
  return `${count} ${t('seconds')}`
}
