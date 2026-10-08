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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerErrorMessage,
  getServerErrorStatus,
} from '@/lib/server-error-message'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  getXCodeAuthorization,
  mutateXCodeAuthorization,
  xcodeAuthorizationQueryKey,
} from '../../api'
import { formatDuration, formatResetPeriod, formatTimestamp } from '../../lib'
import type { PlanRecord, XCodeAuthorizationRequest } from '../../types'

interface Props {
  userId: number
  plans: PlanRecord[]
  plansReady: boolean
  onRefresh: () => Promise<void>
}

export function XCodeAuthorizationSection(props: Props) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const headingId = useId()
  const planInputId = useId()
  const resetSwitchId = useId()
  const [selectedPlanId, setSelectedPlanId] = useState('')
  const [confirmation, setConfirmation] =
    useState<XCodeAuthorizationRequest | null>(null)
  const [advanceResetTime, setAdvanceResetTime] = useState(true)
  useSystemConfigStore((state) => state.config.currency)
  const authorization = useQuery({
    queryKey: xcodeAuthorizationQueryKey(props.userId),
    queryFn: () => getXCodeAuthorization(props.userId),
    retry: false,
  })
  const mutation = useMutation({
    mutationFn: (request: XCodeAuthorizationRequest) =>
      mutateXCodeAuthorization(props.userId, request),
    retry: false,
    onSuccess: async () => {
      setConfirmation(null)
      toast.success(t('XCode authorization updated'))
      await props.onRefresh()
    },
    onError: async (error) => {
      handleServerError(error, t('Operation failed'))
      const status = getServerErrorStatus(error)
      if (status === 409 || status === 503) {
        setConfirmation(null)
        await props.onRefresh()
      }
    },
  })
  const snapshot = authorization.data
  const eligiblePlans = props.plans.filter(
    (record) =>
      record.plan.enabled &&
      record.plan.max_purchase_per_user === 0 &&
      Number.isSafeInteger(record.plan.id)
  )
  const planId = selectedPlanId || snapshot?.subscription?.plan_id || ''
  const selectedPlan = eligiblePlans.find(
    (record) => String(record.plan.id) === planId
  )?.plan
  const current =
    snapshot?.status === 'active' || snapshot?.status === 'exhausted'
  const disabled =
    mutation.isPending || authorization.isFetching || !props.plansReady
  const actionLabels = {
    grant: t('Grant authorization'),
    renew: t('Renew authorization'),
    replace: t('Replace authorization'),
    revoke: t('Revoke authorization'),
    reset: t('Reset designated quota'),
  }
  const statusLabels = {
    unassigned: t('Unassigned'),
    active: t('Active'),
    exhausted: t('Exhausted'),
    expired: t('Expired'),
    revoked: t('Revoked'),
  }
  const actionDescriptions = {
    grant: t(
      'Create a new XCode-only subscription from the selected plan. No wallet charge or payment order is created.'
    ),
    renew: t(
      'Extend an active authorization by the current plan duration without replenishing its quota or changing its cycle. An expired authorization creates a new subscription.'
    ),
    replace: t(
      'Cancel the current designated subscription and create a new one from the selected plan. Unused quota is not carried over.'
    ),
    revoke: t(
      'Cancel the designated subscription immediately and preserve its history. New XCode model requests will be blocked.'
    ),
    reset: t(
      'Reset quota only for the designated subscription. Other subscriptions for this user and plan are not reset.'
    ),
  }

  function requestAction(action: XCodeAuthorizationRequest['action']) {
    if (!snapshot || disabled) return
    const expected_revision = snapshot.authorization_revision
    if (action === 'reset') {
      setAdvanceResetTime(true)
      setConfirmation({ action, expected_revision, advance_reset_time: true })
    } else if (action === 'revoke') {
      setConfirmation({ action, expected_revision })
    } else if (selectedPlan) {
      setConfirmation({
        action,
        expected_revision,
        plan_id: String(selectedPlan.id),
      })
    }
  }

  function displayQuota(value: string | null): string {
    if (value === null) return t('Unlimited')
    // Currency helpers accept numbers; keep larger counters exact in raw quota units.
    if (BigInt(value) > BigInt(Number.MAX_SAFE_INTEGER)) {
      return t('{{value}} quota', { value })
    }
    return formatQuotaWithCurrency(Number(value), { locale })
  }

  const quota = snapshot?.quota
  let cycle = '-'
  if (quota?.reset_period === 'custom') {
    cycle = `${quota.reset_custom_seconds} ${t('seconds')}`
  } else if (quota) {
    cycle = formatResetPeriod({ quota_reset_period: quota.reset_period }, t)
  }

  let templateQuota = t('Exact quota is shown in the authorization snapshot.')
  if (selectedPlan?.total_amount === 0) {
    templateQuota = t('Unlimited')
  } else if (selectedPlan && Number.isSafeInteger(selectedPlan.total_amount)) {
    templateQuota = displayQuota(String(selectedPlan.total_amount))
  }

  return (
    <section
      aria-labelledby={headingId}
      aria-busy={authorization.isFetching || mutation.isPending}
      className='space-y-3 rounded-md border p-4'
    >
      <h3 id={headingId} className='text-base font-medium'>
        {t('XCode designated authorization')}
      </h3>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Ordinary subscription binding does not designate XCode authorization. XCode uses only its designated subscription, never the wallet or other subscriptions.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Plan group changes affect this user across all native clients, not just XCode.'
        )}
      </p>
      {authorization.isPending && <LoadingState className='min-h-24' />}
      {authorization.isError && (
        <ErrorState
          className='min-h-24'
          title={t('Unable to load XCode authorization')}
          description={getServerErrorMessage(
            authorization.error,
            t('Loading failed')
          )}
          onRetry={() => {
            void authorization.refetch()
          }}
        />
      )}
      {!authorization.isPending && !authorization.isError && snapshot && (
        <>
          <div
            aria-live='polite'
            className='flex flex-wrap items-center justify-between gap-2'
          >
            <StatusBadge
              label={statusLabels[snapshot.status]}
              variant={snapshot.status === 'active' ? 'success' : 'neutral'}
              copyable={false}
            />
            <span className='text-muted-foreground text-xs break-all'>
              {t('Revision')}: {snapshot.authorization_revision}
            </span>
          </div>
          {snapshot.subscription ? (
            <dl className='grid grid-cols-1 gap-3 text-sm sm:grid-cols-2'>
              {[
                [
                  t('Plan'),
                  snapshot.subscription.plan_title ||
                    `#${snapshot.subscription.plan_id}`,
                ],
                [t('Subscription ID'), snapshot.subscription.id],
                [
                  t('Start'),
                  snapshot.subscription.start_time === null
                    ? '-'
                    : formatTimestamp(snapshot.subscription.start_time / 1000),
                ],
                [
                  t('End'),
                  snapshot.subscription.end_time === null
                    ? '-'
                    : formatTimestamp(snapshot.subscription.end_time / 1000),
                ],
                [t('Total Quota'), quota ? displayQuota(quota.total) : '-'],
                [t('Used'), quota ? displayQuota(quota.used) : '-'],
                [t('Remaining'), quota ? displayQuota(quota.remaining) : '-'],
                [t('Reset cycle'), cycle],
                [
                  t('Last reset'),
                  quota?.last_reset_time == null
                    ? '-'
                    : formatTimestamp(quota.last_reset_time / 1000),
                ],
                [
                  t('Next reset'),
                  quota?.next_reset_time == null
                    ? '-'
                    : formatTimestamp(quota.next_reset_time / 1000),
                ],
              ].map(([label, value]) => (
                <div key={label} className='min-w-0'>
                  <dt className='text-muted-foreground'>{label}</dt>
                  <dd className='break-words tabular-nums'>{value}</dd>
                </div>
              ))}
            </dl>
          ) : (
            <EmptyState
              className='min-h-24'
              title={t('No XCode authorization')}
            />
          )}
          {quota && (
            <p className='text-muted-foreground text-xs'>
              {t('Reset time zone')}: {quota.reset_time_zone} (
              {t('UTC offset (seconds)')}:{' '}
              {formatNumber(quota.reset_utc_offset_seconds, locale)}).{' '}
              {t(
                'Reset times are set by the server; displayed dates use your local time zone.'
              )}
            </p>
          )}
          <div className='space-y-2'>
            <label htmlFor={planInputId} className='text-sm font-medium'>
              {t('XCode authorization plan')}
            </label>
            <Combobox
              id={planInputId}
              aria-label={t('XCode authorization plan')}
              className='w-full'
              options={eligiblePlans.map((record) => ({
                value: String(record.plan.id),
                label: record.plan.title,
              }))}
              value={selectedPlan ? planId : ''}
              onValueChange={(value) => setSelectedPlanId(value ?? '')}
              disabled={disabled}
              placeholder={t('Select subscription plan')}
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Only enabled plans with no per-user purchase limit can be designated.'
              )}
            </p>
            <p className='text-muted-foreground text-xs'>
              {t('Selected plan template')}
            </p>
            {selectedPlan && (
              <p className='text-muted-foreground text-sm break-words'>
                {t('Duration')}:{' '}
                {selectedPlan.duration_unit === 'custom'
                  ? `${selectedPlan.custom_seconds || 0} ${t('seconds')}`
                  : formatDuration(selectedPlan, t)}{' '}
                · {t('Total Quota')}: {templateQuota} · {t('Reset cycle')}:{' '}
                {selectedPlan.quota_reset_period === 'custom'
                  ? `${selectedPlan.quota_reset_custom_seconds || 0} ${t('seconds')}`
                  : formatResetPeriod(selectedPlan, t)}
              </p>
            )}
          </div>
          <div className='flex flex-wrap gap-2'>
            {!current && (
              <Button
                size='sm'
                disabled={disabled || !selectedPlan}
                onClick={() => requestAction('grant')}
              >
                {actionLabels.grant}
              </Button>
            )}
            {(current || snapshot.status === 'expired') && (
              <Button
                size='sm'
                variant='outline'
                disabled={
                  disabled ||
                  !selectedPlan ||
                  planId !== snapshot.subscription?.plan_id
                }
                onClick={() => requestAction('renew')}
              >
                {actionLabels.renew}
              </Button>
            )}
            {current && (
              <>
                <Button
                  size='sm'
                  variant='outline'
                  disabled={
                    disabled ||
                    !selectedPlan ||
                    planId === snapshot.subscription?.plan_id
                  }
                  onClick={() => requestAction('replace')}
                >
                  {actionLabels.replace}
                </Button>
                <Button
                  size='sm'
                  variant='outline'
                  disabled={disabled}
                  onClick={() => requestAction('reset')}
                >
                  {actionLabels.reset}
                </Button>
                <Button
                  size='sm'
                  variant='destructive'
                  disabled={disabled}
                  onClick={() => requestAction('revoke')}
                >
                  {actionLabels.revoke}
                </Button>
              </>
            )}
          </div>
        </>
      )}
      {confirmation && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open && !mutation.isPending) setConfirmation(null)
          }}
          title={actionLabels[confirmation.action]}
          confirmText={actionLabels[confirmation.action]}
          desc={
            <div className='space-y-2'>
              <p>{actionDescriptions[confirmation.action]}</p>
              {'plan_id' in confirmation && (
                <p>
                  {t('Plan')}: {selectedPlan?.title}
                </p>
              )}
              <p>
                {t(
                  'Plan group changes affect this user across all native clients, not just XCode.'
                )}
              </p>
            </div>
          }
          isLoading={mutation.isPending}
          disabled={
            authorization.isError ||
            authorization.isFetching ||
            confirmation.expected_revision !== snapshot?.authorization_revision
          }
          destructive={
            confirmation.action === 'revoke' ||
            confirmation.action === 'replace'
          }
          handleConfirm={() =>
            mutation.mutate(
              confirmation.action === 'reset'
                ? { ...confirmation, advance_reset_time: advanceResetTime }
                : confirmation
            )
          }
        >
          {confirmation.action === 'reset' && (
            <div className='flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm'>
              <label htmlFor={resetSwitchId}>
                {t('Advance next reset time')}
              </label>
              <Switch
                id={resetSwitchId}
                checked={advanceResetTime}
                disabled={mutation.isPending}
                onCheckedChange={(checked) => setAdvanceResetTime(!!checked)}
                aria-label={t('Advance next reset time')}
              />
            </div>
          )}
        </ConfirmDialog>
      )}
    </section>
  )
}
