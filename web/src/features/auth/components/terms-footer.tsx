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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import type { SystemStatus } from '../types'

interface TermsFooterProps {
  variant?: 'sign-in' | 'sign-up' | 'links'
  className?: string
  status?: SystemStatus | null
}

export function TermsFooter(props: TermsFooterProps) {
  const { t } = useTranslation()
  const variant = props.variant ?? 'sign-in'
  const linksOnly = variant === 'links'
  // Explicit links mode is for pages with known configured local documents.
  const showDefaults = linksOnly && props.status === undefined
  const hasUserAgreement =
    showDefaults || Boolean(props.status?.user_agreement_enabled)
  const hasPrivacyPolicy =
    showDefaults || Boolean(props.status?.privacy_policy_enabled)

  const activeLinks = []
  if (hasUserAgreement) {
    activeLinks.push({
      label: t('User Agreement'),
      href: '/user-agreement',
    })
  }
  if (hasPrivacyPolicy) {
    activeLinks.push({
      label: t('Privacy Policy'),
      href: '/privacy-policy',
    })
  }
  if (activeLinks.length === 0) {
    return null
  }

  const [firstLink, secondLink] = activeLinks

  return (
    <p
      className={cn(
        'text-muted-foreground text-center text-xs',
        props.className
      )}
    >
      {!linksOnly && (
        <>
          {variant === 'sign-in'
            ? t('By clicking sign in, you agree to our')
            : t('By creating an account, you agree to our')}{' '}
        </>
      )}
      <a
        href={firstLink.href}
        target={linksOnly ? '_blank' : undefined}
        rel={linksOnly ? 'noopener noreferrer' : undefined}
        className='hover:text-primary underline underline-offset-4'
      >
        {firstLink.label}
      </a>
      {secondLink && (
        <>
          {' '}
          {t('and')}{' '}
          <a
            href={secondLink.href}
            target={linksOnly ? '_blank' : undefined}
            rel={linksOnly ? 'noopener noreferrer' : undefined}
            className='hover:text-primary underline underline-offset-4'
          >
            {secondLink.label}
          </a>
        </>
      )}
      {!linksOnly && '.'}
    </p>
  )
}
