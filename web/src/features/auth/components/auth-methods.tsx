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
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useMediaQuery } from '@/hooks/use-media-query'

import type { SystemStatus } from '../types'
import { LegalConsent } from './legal-consent'
import { WechatOpenLogin } from './wechat-open-login'

type AuthMethodsProps = {
  children: ReactNode
  status: SystemStatus | null
  agreed: boolean
  onAgree: (agreed: boolean) => void
  redirectTo?: string
}

export function AuthMethods(props: AuthMethodsProps) {
  const { t } = useTranslation()
  const narrow = useMediaQuery('(max-width: 767px)')
  const [method, setMethod] = useState('account')
  const capability = props.status?.wechat_open
  const websiteEnabled = Boolean(capability?.enabled)
  const phone = /Android|iPhone|iPod|Mobile/i.test(navigator.userAgent)
  const consentGranted =
    (!props.status?.user_agreement_enabled &&
      !props.status?.privacy_policy_enabled) ||
    props.agreed
  const websiteLogin =
    capability?.enabled && !phone ? (
      <WechatOpenLogin
        capability={capability}
        consentGranted={consentGranted}
        redirectTo={props.redirectTo}
        registrationEnabled={
          props.status?.register_enabled ?? props.status?.data?.register_enabled
        }
      />
    ) : null

  return (
    <div className='space-y-6'>
      {websiteEnabled && narrow && !phone ? (
        <Tabs value={method} onValueChange={setMethod} className='gap-6'>
          <TabsList
            className='grid h-auto w-full grid-cols-2'
            aria-label={t('Login method')}
          >
            <TabsTrigger value='account' className='whitespace-normal'>
              {t('Account login')}
            </TabsTrigger>
            <TabsTrigger value='wechat-open' className='whitespace-normal'>
              {t('WeChat login / registration')}
            </TabsTrigger>
          </TabsList>
          <TabsContent value='account' keepMounted>
            {props.children}
          </TabsContent>
          <TabsContent value='wechat-open'>{websiteLogin}</TabsContent>
        </Tabs>
      ) : (
        <div
          className={
            websiteEnabled && !phone
              ? 'grid min-w-0 gap-7 md:grid-cols-2 md:gap-8'
              : undefined
          }
        >
          <div className='min-w-0'>{props.children}</div>
          {websiteLogin && (
            <div className='min-w-0 md:border-s md:ps-8'>{websiteLogin}</div>
          )}
        </div>
      )}
      {websiteEnabled && phone && (
        <p role='note' className='text-muted-foreground text-center text-sm'>
          {t(
            'To use WeChat login, open this page on a computer and scan with your phone. You can use account login here.'
          )}
        </p>
      )}
      <LegalConsent
        status={props.status}
        checked={props.agreed}
        onCheckedChange={props.onAgree}
      />
    </div>
  )
}
