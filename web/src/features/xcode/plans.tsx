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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LanguageMenu } from '@/components/language-menu'
import { Main } from '@/components/layout/components/main'
import { Section } from '@/components/layout/components/section'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { ThemeSwitch } from '@/components/theme-switch'
import { Progress } from '@/components/ui/progress'
import { TitledCard } from '@/components/ui/titled-card'
import { toIntlLocale } from '@/i18n/languages'
import { DEFAULT_LOGO, DEFAULT_SYSTEM_NAME } from '@/lib/constants'
import { formatNumber } from '@/lib/format'

import {
  fetchXCodeWebView,
  XCodeWebViewError,
  type XCodeWebView,
} from './plans-api'
import {
  formatXCodeQuota,
  formatXCodeReferencePrice,
  formatXCodeResetPeriod,
} from './plans-format'

export function XCodePlans() {
  const { t } = useTranslation()
  const view = useQuery({
    queryKey: ['xcode', 'web', 'view'],
    queryFn: ({ signal }) => fetchXCodeWebView(signal),
    retry: false,
    gcTime: 0,
    staleTime: 0,
    refetchOnMount: 'always',
    refetchOnWindowFocus: true,
    meta: { errorToast: false },
  })

  let content
  if (view.isPending) {
    content = <LoadingState message={t('Loading XCode plans...')} />
  } else if (view.isError) {
    const reason =
      view.error instanceof XCodeWebViewError
        ? view.error.reason
        : 'unavailable'
    let title = t('XCode plans are temporarily unavailable')
    let description = t(
      'The server could not load this read-only view. Try again later.'
    )
    if (reason === 'expired') {
      title = t('This read-only session has expired or been revoked')
      description = t(
        'Open plans again from XCode to start a new read-only session.'
      )
    } else if (reason === 'invalid') {
      title = t('This read-only view is invalid')
      description = t(
        'Open plans again from XCode to start a new read-only session.'
      )
    }
    content = <ErrorState title={title} description={description} />
  } else {
    content = <PlansSnapshot view={view.data} />
  }

  return (
    <div className='bg-background text-foreground min-h-svh'>
      <header className='border-b'>
        <div className='mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-4 sm:px-6'>
          <div className='flex min-w-0 items-center gap-2'>
            <img
              src={DEFAULT_LOGO}
              alt={t('Logo')}
              className='size-7 shrink-0 rounded-md'
            />
            <span className='truncate font-semibold'>
              {DEFAULT_SYSTEM_NAME}
            </span>
          </div>
          <div className='flex shrink-0 items-center gap-1'>
            <LanguageMenu />
            <ThemeSwitch />
          </div>
        </div>
      </header>
      <Main className='mx-auto w-full max-w-6xl overflow-visible'>
        <Section className='space-y-6 py-6 sm:px-6 sm:py-8 md:py-10'>
          <div className='max-w-3xl space-y-2'>
            <h1 className='text-2xl font-semibold tracking-tight sm:text-3xl'>
              {t('XCode plans')}
            </h1>
            <p className='text-muted-foreground text-sm leading-relaxed'>
              {t(
                'Read-only plan references and your designated XCode authorization. Contact an administrator for grants, renewals or quota resets. No purchases or changes are available here.'
              )}
            </p>
          </div>
          <div aria-live='polite' aria-busy={view.isFetching}>
            {content}
          </div>
        </Section>
      </Main>
      <footer className='text-muted-foreground mx-auto max-w-6xl px-4 py-6 text-xs sm:px-6'>
        New API · © 2023–2026 QuantumNous
      </footer>
    </div>
  )
}

function PlansSnapshot(props: { view: XCodeWebView }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const status = props.view.entitlements.status
  const statusLabels = {
    unassigned: t('Unassigned'),
    active: t('Active'),
    exhausted: t('Exhausted'),
    expired: t('Expired'),
    revoked: t('Revoked'),
  }
  const subscription = props.view.entitlements.subscription
  const quota = props.view.entitlements.quota
  const dateFormat = new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
  const unitLabels = {
    year: t('years'),
    month: t('months'),
    day: t('days'),
    hour: t('hours'),
    custom: t('seconds'),
  }

  return (
    <div className='space-y-8'>
      <TitledCard
        title={t('XCode designated authorization')}
        description={t(
          'XCode uses only this designated subscription, never your wallet or other subscriptions.'
        )}
        action={
          <StatusBadge
            label={statusLabels[status]}
            variant={status === 'active' ? 'success' : 'neutral'}
            copyable={false}
          />
        }
        disableHoverEffect
      >
        {subscription && quota ? (
          <div className='space-y-5'>
            <p className='text-lg font-medium break-words'>
              {subscription.plan_title || `#${subscription.plan_id}`}
            </p>
            <dl className='grid gap-4 text-sm sm:grid-cols-3'>
              {[
                [
                  t('Total Quota'),
                  formatXCodeQuota(quota.total, props.view.display, t, locale),
                ],
                [
                  t('Used'),
                  formatXCodeQuota(quota.used, props.view.display, t, locale),
                ],
                [
                  t('Remaining'),
                  formatXCodeQuota(
                    quota.remaining,
                    props.view.display,
                    t,
                    locale
                  ),
                ],
              ].map(([label, value]) => (
                <div key={label} className='min-w-0'>
                  <dt className='text-muted-foreground'>{label}</dt>
                  <dd className='mt-1 text-xl font-semibold break-words tabular-nums'>
                    {value}
                  </dd>
                </div>
              ))}
            </dl>
            {quota.used_percentage !== null && (
              <Progress
                value={quota.used_percentage}
                aria-label={t('Used quota percentage')}
              />
            )}
            <dl className='grid gap-4 text-sm sm:grid-cols-2 lg:grid-cols-3'>
              {[
                [
                  t('Start'),
                  subscription.start_time === null
                    ? t('Not available')
                    : dateFormat.format(subscription.start_time),
                ],
                [
                  t('End'),
                  subscription.end_time === null
                    ? t('Not available')
                    : dateFormat.format(subscription.end_time),
                ],
                [
                  t('Reset cycle'),
                  formatXCodeResetPeriod(
                    quota.reset_period,
                    quota.reset_custom_seconds,
                    t,
                    locale
                  ),
                ],
                [
                  t('Last reset'),
                  quota.last_reset_time === null
                    ? t('Not available')
                    : dateFormat.format(quota.last_reset_time),
                ],
                [
                  t('Next reset'),
                  quota.next_reset_time === null
                    ? t('No reset scheduled')
                    : dateFormat.format(quota.next_reset_time),
                ],
              ].map(([label, value]) => (
                <div key={label} className='min-w-0'>
                  <dt className='text-muted-foreground'>{label}</dt>
                  <dd className='mt-1 break-words tabular-nums'>{value}</dd>
                </div>
              ))}
            </dl>
            <p className='text-muted-foreground text-xs leading-relaxed break-words'>
              {t('Reset time zone')}: {quota.reset_time_zone} (
              {t('UTC offset (seconds)')}:{' '}
              {formatNumber(quota.reset_utc_offset_seconds, locale)}).{' '}
              {t(
                'Reset times are set by the server; displayed dates use your local time zone.'
              )}
            </p>
          </div>
        ) : (
          <EmptyState
            className='min-h-32'
            title={t('No XCode authorization')}
            description={t(
              'Contact an administrator to request a designated XCode plan.'
            )}
          />
        )}
      </TitledCard>
      <section aria-labelledby='xcode-plan-templates' className='space-y-4'>
        <div className='space-y-1'>
          <h2 id='xcode-plan-templates' className='text-xl font-semibold'>
            {t('Plan templates')}
          </h2>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Prices are template references in the listed currency, not a checkout offer.'
            )}
          </p>
        </div>
        {props.view.plans.items.length === 0 ? (
          <EmptyState
            bordered
            title={t('No plans available')}
            description={t(
              'Contact an administrator for available XCode plans.'
            )}
          />
        ) : (
          <div className='grid items-start gap-4 md:grid-cols-2 lg:grid-cols-3'>
            {props.view.plans.items.map((plan) => {
              const duration =
                plan.duration_unit === 'custom'
                  ? plan.duration_custom_seconds
                  : plan.duration_value
              const durationCount =
                BigInt(duration) > BigInt(Number.MAX_SAFE_INTEGER)
                  ? duration
                  : formatNumber(Number(duration), locale)
              return (
                <TitledCard
                  key={plan.id}
                  title={plan.title || `#${plan.id}`}
                  description={plan.subtitle || undefined}
                  titleClassName='break-words'
                  descriptionClassName='break-words'
                  disableHoverEffect
                >
                  <dl className='space-y-4 text-sm'>
                    {[
                      [
                        t('Reference price'),
                        formatXCodeReferencePrice(plan, locale),
                      ],
                      [
                        t('Duration'),
                        `${durationCount} ${unitLabels[plan.duration_unit]}`,
                      ],
                      [
                        t('Total Quota'),
                        formatXCodeQuota(
                          plan.total_quota === '0' ? null : plan.total_quota,
                          props.view.display,
                          t,
                          locale
                        ),
                      ],
                      [
                        t('Reset cycle'),
                        formatXCodeResetPeriod(
                          plan.reset_period,
                          plan.reset_custom_seconds,
                          t,
                          locale
                        ),
                      ],
                    ].map(([label, value]) => (
                      <div key={label} className='min-w-0'>
                        <dt className='text-muted-foreground'>{label}</dt>
                        <dd className='mt-1 font-medium break-words tabular-nums'>
                          {value}
                        </dd>
                      </div>
                    ))}
                  </dl>
                </TitledCard>
              )
            })}
          </div>
        )}
      </section>
      <p className='text-muted-foreground text-xs'>
        {t('Snapshot time')}: {dateFormat.format(props.view.server_time)}
      </p>
    </div>
  )
}
