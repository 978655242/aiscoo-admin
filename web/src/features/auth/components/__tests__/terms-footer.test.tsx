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
import { act, cleanup, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import { TermsFooter } from '../terms-footer'

beforeEach(async () => {
  await i18next.changeLanguage('en')
  i18next.addResourceBundle('zh', 'translation', zh.translation)
})

afterEach(async () => {
  cleanup()
  await i18next.changeLanguage('en')
  i18next.removeResourceBundle('zh', 'translation')
})

it('shows both stable documents without consent when links mode has no status', () => {
  const { container } = render(<TermsFooter variant='links' />)
  expect(screen.getAllByRole('link')).toHaveLength(2)
  expect(screen.getByRole('link', { name: 'User Agreement' })).toHaveAttribute(
    'href',
    '/user-agreement'
  )
  expect(screen.getByRole('link', { name: 'Privacy Policy' })).toHaveAttribute(
    'href',
    '/privacy-policy'
  )
  expect(container).not.toHaveTextContent(/you agree|by clicking|by creating/i)
  for (const link of screen.getAllByRole('link')) {
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  }
})

it.each([
  null,
  { user_agreement_enabled: false, privacy_policy_enabled: false },
])('hides links when explicitly supplied status is %j', (status) => {
  const { container } = render(<TermsFooter variant='links' status={status} />)
  expect(container).toBeEmptyDOMElement()
})

it.each(['sign-in', 'sign-up'] as const)(
  'keeps %s hidden when no status is provided',
  (variant) => {
    const { container, rerender } = render(<TermsFooter variant={variant} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<TermsFooter variant={variant} status={null} />)
    expect(container).toBeEmptyDOMElement()
    rerender(
      <TermsFooter
        variant={variant}
        status={{
          user_agreement_enabled: false,
          privacy_policy_enabled: false,
        }}
      />
    )
    expect(container).toBeEmptyDOMElement()
  }
)

it.each([
  [{ user_agreement_enabled: true }, 'User Agreement', '/user-agreement'],
  [{ privacy_policy_enabled: true }, 'Privacy Policy', '/privacy-policy'],
] as const)(
  'renders only the enabled document for status %j',
  (status, name, href) => {
    const { rerender } = render(<TermsFooter status={status} />)
    expect(screen.getAllByRole('link')).toHaveLength(1)
    expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
    expect(screen.getByRole('link', { name })).not.toHaveAttribute('target')
    rerender(<TermsFooter variant='links' status={status} />)
    expect(screen.getAllByRole('link')).toHaveLength(1)
    expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
    expect(screen.getByRole('link', { name })).toHaveAttribute(
      'target',
      '_blank'
    )
  }
)

it.each([
  [
    'sign-in',
    'By clicking sign in, you agree to our',
    '点击登录即表示您同意我们的',
  ],
  [
    'sign-up',
    'By creating an account, you agree to our',
    '创建账户即表示您同意我们的',
  ],
] as const)(
  'translates the %s notice and document labels when the language changes',
  async (variant, english, chinese) => {
    const { container } = render(
      <TermsFooter
        variant={variant}
        status={{ user_agreement_enabled: true, privacy_policy_enabled: true }}
      />
    )
    expect(container).toHaveTextContent(english)
    for (const link of screen.getAllByRole('link')) {
      expect(link).not.toHaveAttribute('target')
    }
    await act(async () => {
      await i18next.changeLanguage('zh')
    })
    expect(container).toHaveTextContent(chinese)
    expect(screen.getByRole('link', { name: '用户协议' })).toBeVisible()
    expect(screen.getByRole('link', { name: '隐私政策' })).toBeVisible()
    expect(container).toHaveTextContent('和')
  }
)
