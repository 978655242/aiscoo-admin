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

import i18n from '@/i18n/config'
import { toIntlLocale } from '@/i18n/languages'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { useSystemConfigStore } from '@/stores/system-config-store'

import type { XCodeWebPlan, XCodeWebView } from '../plans-api'
import {
  formatXCodeQuota,
  formatXCodeReferencePrice,
  formatXCodeResetPeriod,
} from '../plans-format'

const display: XCodeWebView['display'] = {
  quota_per_unit: '500000',
  display_type: 'USD',
  currency: 'USD',
  currency_symbol: '$',
  usd_to_currency_rate: '1',
}
const plan: XCodeWebPlan = {
  id: '1',
  title: 'EUR template',
  subtitle: '',
  price_amount: '19.5',
  currency: 'EUR',
  duration_unit: 'month',
  duration_value: '1',
  duration_custom_seconds: '0',
  total_quota: '0',
  reset_period: 'never',
  reset_custom_seconds: '0',
  enabled: true,
}

test('keeps exact quota when currency conversion or number precision would misrepresent it', async () => {
  await i18n.changeLanguage('en')
  const t = i18n.getFixedT('en')
  const locale = toIntlLocale('en')
  expect(formatXCodeQuota('9007199254740993', display, t, locale)).toBe(
    '9007199254740993 quota'
  )
  expect(formatXCodeQuota('1', display, t, locale)).toBe('1 quota')
  expect(formatXCodeQuota(null, display, t, locale)).toBe('Unlimited')
  expect(
    formatXCodeQuota(
      '500000',
      {
        ...display,
        display_type: 'TOKENS',
        currency: null,
        usd_to_currency_rate: null,
      },
      t,
      locale
    )
  ).toBe('500,000 quota')
  expect(
    formatXCodeReferencePrice(
      { ...plan, price_amount: '9007199254740993.01' },
      locale
    )
  ).toBe('EUR 9007199254740993.01')
  expect(formatXCodeResetPeriod('custom', '9007199254740993', t, locale)).toBe(
    '9007199254740993 seconds'
  )
})

test('renders the readonly currency without changing native dashboard currency', async () => {
  await i18n.changeLanguage('en')
  const original = useSystemConfigStore.getState().config.currency
  const locale = toIntlLocale('en')
  try {
    useSystemConfigStore
      .getState()
      .setConfig({
        currency: { ...original, quotaDisplayType: 'CNY', usdExchangeRate: 7 },
      })
    expect(formatXCodeReferencePrice(plan, locale)).toBe('EUR 19.5')
    expect(
      formatXCodeQuota('500000', display, i18n.getFixedT('en'), locale)
    ).toBe('$1')
    expect(formatCurrencyFromUSD(1, { locale, abbreviate: false })).toBe('¥7')
  } finally {
    useSystemConfigStore.getState().setConfig({ currency: original })
  }
})
