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
import type { TFunction } from 'i18next'
import { Check, Sparkle, Users } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { readXCodePlanDisplayContext } from './display-context'
import {
  fetchXCodeWebView,
  XCodeWebViewError,
  type XCodeWebPlan,
  type XCodeWebView,
} from './plans-api'
import {
  buildPlanCatalog,
  formatPlanPrice,
  type CatalogPlan,
  type PlanCycle,
} from './plans-catalog'
import { formatXCodeReferencePrice } from './plans-format'

// XCODE: React owns only audience/cycle/dialog state; authorization and SKU facts remain server-owned.
export function XCodePlans() {
  const { t } = useTranslation()
  const [audience, setAudience] = useState<'personal' | 'team'>(
    () => readXCodePlanDisplayContext()?.initialAudience ?? 'personal'
  )
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
  let errorTitle = t('XCode plans are temporarily unavailable')
  let errorDescription = t(
    'The server could not load this read-only view. Try again later.'
  )
  if (view.error instanceof XCodeWebViewError) {
    if (view.error.reason === 'expired') {
      errorTitle = t('This read-only session has expired or been revoked')
    }
    if (view.error.reason === 'invalid') {
      errorTitle = t('This read-only view is invalid')
    }
    if (view.error.reason !== 'unavailable') {
      errorDescription = t(
        'Open plans again from XCode to start a new read-only session.'
      )
    }
  }
  return (
    <div className='xcode-plans text-ui-base'>
      <main className='xcode-plans-content'>
        <nav className='xcode-plan-audience' aria-label={t('Plan audience')}>
          <Button
            variant='ghost'
            className='text-ui-base'
            aria-pressed={audience === 'personal'}
            onClick={() => setAudience('personal')}
          >
            {t('Personal')}
          </Button>
          <Button
            variant='ghost'
            className='text-ui-base'
            aria-pressed={audience === 'team'}
            onClick={() => setAudience('team')}
          >
            {t('Team')}
          </Button>
        </nav>
        <div aria-live='polite' aria-busy={view.isFetching}>
          {view.isPending && (
            <div role='status'>
              <LoadingState
                className='xcode-plan-notice'
                message={t('Loading XCode plans...')}
              />
            </div>
          )}
          {view.isError && (
            <div role='alert'>
              <ErrorState
                className='xcode-plan-notice'
                title={errorTitle}
                description={errorDescription}
              />
            </div>
          )}
          {view.data && !view.isError && (
            <PlansSnapshot view={view.data} audience={audience} />
          )}
        </div>
        <footer className='xcode-plan-footer text-ui-sm'>
          New API · © 2023–2026 QuantumNous
        </footer>
      </main>
    </div>
  )
}

function planDuration(
  plan: XCodeWebPlan,
  t: TFunction,
  locale: string | undefined
): string {
  const raw =
    plan.duration_unit === 'custom'
      ? plan.duration_custom_seconds
      : plan.duration_value
  const count =
    BigInt(raw) > BigInt(Number.MAX_SAFE_INTEGER)
      ? raw
      : formatNumber(Number(raw), locale)
  const units = {
    year: t('years'),
    month: t('months'),
    day: t('days'),
    hour: t('hours'),
    custom: t('seconds'),
  }
  return `${count} ${units[plan.duration_unit]}`
}

type CardEntry = {
  key: string
  title: string
  card: CatalogPlan | null
  baseline: CatalogPlan | null
  order: number
}

function PlansSnapshot(props: {
  view: XCodeWebView
  audience: 'personal' | 'team'
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const catalog = useMemo(
    () =>
      buildPlanCatalog(props.view.plans.items, props.view.plan_presentations),
    [props.view.plans.items, props.view.plan_presentations]
  )
  const [cycle, setCycle] = useState<PlanCycle | null>(null)
  const [explanationId, setExplanationId] = useState<string | null>(null)
  const subscription = props.view.entitlements.subscription
  // 默认打开当前授权模板所在周期，让状态与到期时间直接出现在对应套餐卡片上。
  const currentCycle = (['monthly', 'quarterly', 'yearly'] as const).find(
    (option) =>
      catalog.groups.some((group) =>
        group.slots[option].some(
          (card) => card.plan.id === subscription?.plan_id
        )
      )
  )
  const selectedCycle = cycle ?? currentCycle ?? catalog.defaultCycle
  const statusLabels = {
    unassigned: t('Unassigned'),
    active: t('Active'),
    exhausted: t('Exhausted'),
    expired: t('Expired'),
    revoked: t('Revoked'),
  }
  const cycleLabels = {
    monthly: t('Monthly'),
    quarterly: t('Quarterly'),
    yearly: t('Yearly'),
  }
  const dateFormat = new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
  const current = subscription
    ? {
        planId: subscription.plan_id,
        status: statusLabels[props.view.entitlements.status],
        end:
          subscription.end_time === null
            ? t('Not available')
            : dateFormat.format(subscription.end_time),
      }
    : null
  // 当前模板没有卡片可承载（已停用模板或团队视图）时保留摘要，避免授权信息消失。
  const showSummary =
    current !== null &&
    (props.audience === 'team' ||
      (currentCycle === undefined &&
        !catalog.independent.some((card) => card.plan.id === current.planId)))
  const order: Record<string, number> = Object.fromEntries(
    props.view.plans.items.map((plan, index) => [plan.id, index])
  )
  const entries: CardEntry[] = catalog.independent.map((card) => ({
    key: card.plan.id,
    title: card.plan.title || `#${card.plan.id}`,
    card,
    baseline: null,
    order: order[card.plan.id],
  }))
  for (const group of catalog.groups) {
    const cards = selectedCycle ? group.slots[selectedCycle] : []
    const baseline =
      group.slots.monthly.length === 1 ? group.slots.monthly[0] : null
    if (cards.length === 0) {
      const groupOrder = Math.min(
        ...Object.values(group.slots)
          .flat()
          .map((card) => order[card.plan.id])
      )
      entries.push({
        key: JSON.stringify([group.groupKey, group.currency]),
        title: group.groupTitle,
        card: null,
        baseline: null,
        order: groupOrder,
      })
    }
    for (const card of cards) {
      entries.push({
        key: card.plan.id,
        title:
          cards.length > 1
            ? `${card.plan.title || group.groupTitle} (#${card.plan.id})`
            : group.groupTitle,
        card,
        baseline,
        order: order[card.plan.id],
      })
    }
  }
  entries.sort((left, right) => left.order - right.order)
  const selectedCard = entries.find(
    (entry) => entry.card?.plan.id === explanationId
  )?.card
  const explanation = selectedCard?.presentation.assignable
    ? selectedCard
    : null
  const renew = explanation?.plan.id === subscription?.plan_id
  // XCODE: 到期 renew 会新建实例，撤销/未授权只能 grant，不能按 SKU 相等冒充有效实例续期。
  const activeInstance =
    props.view.entitlements.status === 'active' ||
    props.view.entitlements.status === 'exhausted'
  let actionExplanation = t(
    'Grant this exact template: the administrator creates a new authorization instance with the current template budget. This is not a replacement of an active authorization.'
  )
  if (activeInstance) {
    actionExplanation = renew
      ? t(
          'Renew this exact template: only extend the term. Renewal does not replenish the current quota, including an exhausted authorization.'
        )
      : t(
          'Replace with this exact template: no time conversion or proration, and no quota rollover or quota carry-over. A different plan ID is a replacement, even within the same display group.'
        )
  } else if (props.view.entitlements.status === 'expired' && renew) {
    actionExplanation = t(
      'Renew this expired template: the administrator creates a new authorization instance with the current template budget, rather than extending the old instance.'
    )
  }
  return (
    <>
      {showSummary && (
        <section
          className='xcode-plan-summary text-ui-caption'
          aria-label={t('XCode designated authorization')}
        >
          <span className='font-medium'>
            {subscription?.plan_title || `#${current.planId}`}
          </span>
          <span className='xcode-plan-status'>{current.status}</span>
          <span>
            {t('Expires at')}: {current.end}
          </span>
        </section>
      )}
      {props.audience === 'team' ? (
        <EmptyState
          className='xcode-plan-notice'
          icon={Users}
          title={t(
            'This deployment does not provide team plans or organization features.'
          )}
        />
      ) : (
        <>
          {catalog.cycles.length > 0 && (
            <nav className='xcode-plan-cycles' aria-label={t('Plan cycle')}>
              {(['monthly', 'quarterly', 'yearly'] as const).map((option) => (
                <Button
                  key={option}
                  variant='ghost'
                  className='text-ui-base'
                  aria-pressed={selectedCycle === option}
                  onClick={() => setCycle(option)}
                >
                  {cycleLabels[option]}
                </Button>
              ))}
            </nav>
          )}
          {entries.length === 0 ? (
            <EmptyState
              className='xcode-plan-notice'
              title={t('No plans available')}
              description={t(
                'Contact an administrator for available XCode plans.'
              )}
            />
          ) : (
            <div className='xcode-plan-grid-container'>
              <div className='xcode-plan-grid'>
                {entries.map((entry) => (
                  <PlanCard
                    key={entry.key}
                    entry={entry}
                    current={current}
                    onExplain={(card) => setExplanationId(card.plan.id)}
                  />
                ))}
              </div>
            </div>
          )}
        </>
      )}
      <Dialog
        open={explanation !== null && props.audience === 'personal'}
        onOpenChange={(open) => {
          if (!open) setExplanationId(null)
        }}
        title={t('Contact administrator')}
        description={t(
          'Only an administrator can grant, renew or replace this template. This page submits no request or payment.'
        )}
        contentClassName='xcode-plans-dialog text-ui-base'
        titleClassName='text-ui-lg'
        descriptionClassName='text-ui-sm'
        showCloseButton={false}
        footer={
          <Button
            className='text-ui-base'
            variant='outline'
            onClick={() => setExplanationId(null)}
          >
            {t('Close')}
          </Button>
        }
      >
        {explanation && (
          <div className='space-y-4'>
            <h3 className='text-ui-base font-semibold'>
              {explanation.plan.title || `#${explanation.plan.id}`} · #
              {explanation.plan.id}
            </h3>
            <dl className='space-y-2'>
              <div>
                <dt>{t('Duration')}</dt>
                <dd>{planDuration(explanation.plan, t, locale)}</dd>
              </div>
              <div>
                <dt>{t('Reference price')}</dt>
                <dd>{formatXCodeReferencePrice(explanation.plan, locale)}</dd>
              </div>
            </dl>
            <p>{actionExplanation}</p>
          </div>
        )}
      </Dialog>
    </>
  )
}

function PlanCard(props: {
  entry: CardEntry
  current: { planId: string; status: string; end: string } | null
  onExplain: (card: CatalogPlan) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const card = props.entry.card
  const metadata = card?.presentation.metadata
  const price = card ? formatPlanPrice(card, props.entry.baseline) : null
  const current =
    card && props.current?.planId === card.plan.id ? props.current : null
  // XCODE: 文本行允许重复且无 ID，按文本 occurrence 保持 React 身份，不按列表下标复用。
  const lineOccurrences = new Map<string, number>()
  const lineKey = (section: string, line: string) => {
    const text = `${section}:${line}`
    const occurrence = lineOccurrences.get(text) ?? 0
    lineOccurrences.set(text, occurrence + 1)
    return `${text}:${occurrence}`
  }
  let issue = null
  if (card?.issue === 'metadata_conflict') {
    issue = t(
      'Display configuration conflicts; this template is shown independently.'
    )
  }
  if (card?.issue === 'invalid_display') {
    issue = t(
      'Display configuration is invalid; original template terms are preserved.'
    )
  }
  return (
    <article className='xcode-plan-card' aria-label={props.entry.title}>
      <div className='xcode-plan-card-heading'>
        <h2 className='text-ui-lg font-semibold'>{props.entry.title}</h2>
        {metadata?.offer_kind === 'trial' && (
          <span className='xcode-plan-tag text-ui-xs'>{t('Trial')}</span>
        )}
        {current && (
          <span className='xcode-plan-tag text-ui-xs'>{current.status}</span>
        )}
      </div>
      {!card || !price ? (
        <p className='xcode-plan-missing text-ui-caption'>
          {t('No template for this cycle.')}
        </p>
      ) : (
        <>
          {card.plan.subtitle && (
            <p className='xcode-plan-secondary text-ui-caption'>
              {card.plan.subtitle}
            </p>
          )}
          <div className='xcode-plan-price'>
            <strong className='tabular-nums'>
              {formatXCodeReferencePrice(
                {
                  ...card.plan,
                  price_amount: price.monthlyAmount ?? price.totalAmount,
                },
                locale
              )}
            </strong>
            {price.monthlyAmount !== null && (
              <span className='xcode-plan-secondary text-ui-caption'>
                {t('/month')}
              </span>
            )}
          </div>
          <p className='xcode-plan-secondary text-ui-caption'>
            {t('Reference price')}:{' '}
            {formatXCodeReferencePrice(card.plan, locale)}
          </p>
          <p className='xcode-plan-secondary text-ui-caption'>
            {t('Duration')}: {planDuration(card.plan, t, locale)}
          </p>
          {current && (
            <p className='xcode-plan-secondary text-ui-caption'>
              {t('Expires at')}: {current.end}
            </p>
          )}
          {price.savingPercent !== null && (
            <p className='xcode-plan-saving text-ui-caption'>
              {t('Save {{percent}}% versus the comparable monthly template', {
                percent: price.savingPercent,
              })}
            </p>
          )}
          {metadata?.highlights.length ? (
            <ul className='xcode-plan-highlights text-ui-base'>
              {metadata.highlights.map((line) => (
                <li key={lineKey('highlight', line)}>
                  <Sparkle
                    aria-hidden='true'
                    className='mt-0.5 size-4 shrink-0'
                  />
                  <span>{line}</span>
                </li>
              ))}
            </ul>
          ) : null}
          {metadata?.features.length ? (
            <ul className='xcode-plan-features text-ui-caption'>
              {metadata.features.map((line) => (
                <li key={lineKey('feature', line)}>
                  <Check aria-hidden='true' className='size-4 shrink-0' />
                  <span>{line}</span>
                </li>
              ))}
            </ul>
          ) : null}
          <p className='xcode-plan-secondary text-ui-sm'>
            {t(
              'Administrator descriptions are display information, not official XWork service entitlements.'
            )}
          </p>
          {issue && (
            <p className='xcode-plan-config-issue text-ui-sm'>{issue}</p>
          )}
          <div className='xcode-plan-card-action'>
            {card.presentation.assignable ? (
              <Button
                variant='outline'
                className='xcode-plan-cta text-ui-base'
                onClick={() => props.onExplain(card)}
              >
                {t('Contact administrator')}
              </Button>
            ) : (
              <p className='xcode-plan-secondary text-ui-caption'>
                {t(
                  'This template cannot currently be assigned. Contact the administrator to check its configuration.'
                )}
              </p>
            )}
          </div>
        </>
      )}
    </article>
  )
}
