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
import { Link, useSearch } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { useStatus } from '@/hooks/use-status'

import { AuthLayout } from '../auth-layout'
import { TermsFooter } from '../components/terms-footer'
import { sanitizeAuthRedirect } from '../lib/auth-redirect'
import { UserAuthForm } from './components/user-auth-form'

export function SignIn() {
  const { t } = useTranslation()
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })
  const { status } = useStatus()
  const redirectTo =
    sanitizeAuthRedirect(redirect, window.location.origin) ?? undefined

  return (
    <AuthLayout
      title={
        status?.wechat_open?.enabled
          ? t('Sign in / register aiscoo')
          : t('Sign in')
      }
      dualMethods={status?.wechat_open?.enabled}
      description={
        !status?.self_use_mode_enabled &&
        status?.register_enabled !== false && (
          <p>
            {t("Don't have an account?")}{' '}
            <Link
              to='/sign-up'
              search={redirectTo ? { redirect: redirectTo } : {}}
              className='text-primary font-medium hover:underline'
            >
              {t('Sign up')}
            </Link>
          </p>
        )
      }
    >
      <div className='w-full space-y-6'>
        <UserAuthForm redirectTo={redirectTo} />
        <TermsFooter
          variant='sign-in'
          status={status}
          className='text-center'
        />
      </div>
    </AuthLayout>
  )
}
