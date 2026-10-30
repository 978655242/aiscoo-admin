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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, Plus, RotateCcw, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTableRowActionMenu,
  StaticDataTable,
} from '@/components/data-table'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerMessage,
  getServerErrorStatus,
  requireServerSuccess,
} from '@/lib/server-error-message'

import {
  getAdminPlans,
  getUserSubscriptions,
  createUserSubscription,
  invalidateUserSubscription,
  deleteUserSubscription,
  resetUserSubscriptionsByPlan,
  xcodeAuthorizationQueryKey,
} from '../../api'
import { formatTimestamp } from '../../lib'
import type { UserSubscriptionRecord } from '../../types'
import { XCodeAuthorizationSection } from './xcode-authorization-section'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  user: { id: number; username?: string } | null
  onSuccess?: () => void
}

function SubscriptionStatusBadge(props: {
  sub: UserSubscriptionRecord['subscription']
  t: (key: string) => string
}) {
  // eslint-disable-next-line react-hooks/purity
  const now = Date.now() / 1000
  const isExpired = (props.sub.end_time || 0) > 0 && props.sub.end_time < now
  const isActive = props.sub.status === 'active' && !isExpired
  if (isActive) {
    return (
      <StatusBadge
        label={props.t('Active')}
        variant='success'
        copyable={false}
      />
    )
  }
  if (props.sub.status === 'cancelled') {
    return (
      <StatusBadge
        label={props.t('Invalidated')}
        variant='neutral'
        copyable={false}
      />
    )
  }
  return (
    <StatusBadge
      label={props.t('Expired')}
      variant='neutral'
      copyable={false}
    />
  )
}

export function UserSubscriptionsDialog(props: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const userId = props.user?.id || 0
  const plansQuery = useQuery({
    queryKey: ['admin-subscription-plans'],
    queryFn: async () => requireServerSuccess(await getAdminPlans()).data || [],
    enabled: props.open && !!userId,
  })
  const subscriptionsQuery = useQuery({
    queryKey: ['admin-user-subscriptions', userId],
    queryFn: async () =>
      requireServerSuccess(await getUserSubscriptions(userId)).data || [],
    enabled: props.open && !!userId,
  })
  const plans = useMemo(() => plansQuery.data || [], [plansQuery.data])
  const subs = subscriptionsQuery.data || []
  const loading = plansQuery.isFetching || subscriptionsQuery.isFetching
  const [creating, setCreating] = useState(false)
  const [selectedPlanId, setSelectedPlanId] = useState<string>('')
  const [resetting, setResetting] = useState(false)
  const [advanceResetTime, setAdvanceResetTime] = useState(true)
  const [resetAction, setResetAction] = useState<{
    planId: number
    planTitle: string
  } | null>(null)
  const [confirmAction, setConfirmAction] = useState<{
    type: 'invalidate' | 'delete'
    subId: number
  } | null>(null)
  const [confirming, setConfirming] = useState(false)

  const planTitleMap = useMemo(() => {
    const map = new Map<number, string>()
    plans.forEach((p) => {
      if (p.plan.id) map.set(p.plan.id, p.plan.title || `#${p.plan.id}`)
    })
    return map
  }, [plans])

  const loadData = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['admin-subscription-plans'] }),
      queryClient.invalidateQueries({
        queryKey: ['admin-user-subscriptions', userId],
      }),
      queryClient.invalidateQueries({
        queryKey: xcodeAuthorizationQueryKey(userId),
      }),
    ])
  }, [queryClient, userId])

  useEffect(() => {
    if (props.open && props.user?.id) {
      setSelectedPlanId('')
    }
  }, [props.open, props.user?.id])

  const handleCreate = async () => {
    if (creating) return
    if (!props.user?.id || !selectedPlanId) {
      toast.error(t('Please select a subscription plan'))
      return
    }
    setCreating(true)
    try {
      const res = await createUserSubscription(props.user.id, {
        plan_id: Number(selectedPlanId),
      })
      if (res.success) {
        toast.success(getServerMessage(res.data) || t('Added successfully'))
        setSelectedPlanId('')
        await loadData()
        props.onSuccess?.()
      } else {
        handleServerError(res)
      }
    } catch (error) {
      handleServerError(error, t('Request failed'))
      if (getServerErrorStatus(error) === 503) await loadData()
    } finally {
      setCreating(false)
    }
  }

  const handleConfirmAction = async () => {
    if (!confirmAction || confirming) return
    if (
      confirmAction.type === 'delete' &&
      subs.some(
        (record) =>
          record.subscription.id === confirmAction.subId &&
          record.subscription.usage_scope === 'xcode'
      )
    ) {
      toast.error(
        t(
          'aiscoo subscription history cannot be deleted here. Cancel or revoke it instead; the server checks all references.'
        )
      )
      setConfirmAction(null)
      return
    }
    setConfirming(true)
    try {
      if (confirmAction.type === 'invalidate') {
        const res = await invalidateUserSubscription(confirmAction.subId)
        if (res.success) {
          toast.success(getServerMessage(res.data) || t('Has been invalidated'))
          await loadData()
          props.onSuccess?.()
        } else {
          handleServerError(res)
        }
      } else {
        const res = await deleteUserSubscription(confirmAction.subId)
        if (res.success) {
          toast.success(t('Deleted'))
          await loadData()
          props.onSuccess?.()
        } else {
          handleServerError(res)
        }
      }
    } catch (error) {
      handleServerError(error, t('Operation failed'))
      const status = getServerErrorStatus(error)
      if (status === 409 || status === 503) await loadData()
    } finally {
      setConfirming(false)
      setConfirmAction(null)
    }
  }

  const handleResetConfirm = async () => {
    if (!props.user?.id || !resetAction || resetting) return
    setResetting(true)
    try {
      const res = await resetUserSubscriptionsByPlan(props.user.id, {
        plan_id: resetAction.planId,
        advance_reset_time: advanceResetTime,
      })
      if (res.success) {
        toast.success(
          t('Reset {{count}} active subscriptions', {
            count: res.data?.reset_count || 0,
          })
        )
        await loadData()
        props.onSuccess?.()
      } else {
        handleServerError(res)
      }
    } catch (error) {
      handleServerError(error, t('Operation failed'))
      const status = getServerErrorStatus(error)
      if (status === 409 || status === 503) await loadData()
    } finally {
      setResetting(false)
      setResetAction(null)
    }
  }

  return (
    <>
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('User Subscription Management')}</SheetTitle>
            <SheetDescription>
              {props.user?.username || '-'} (ID: {props.user?.id || '-'})
            </SheetDescription>
          </SheetHeader>

          <div className={sideDrawerFormClassName()}>
            {props.open && props.user && (
              <XCodeAuthorizationSection
                key={props.user.id}
                userId={props.user.id}
                plans={plans}
                plansReady={plansQuery.isSuccess && !plansQuery.isFetching}
                onRefresh={async () => {
                  await loadData()
                  props.onSuccess?.()
                }}
              />
            )}
            <div className='space-y-2'>
              <h3 className='text-base font-medium'>
                {t('General subscriptions')}
              </h3>
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Ordinary subscription binding does not designate aiscoo authorization.'
                )}
              </p>
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Native quota reset applies to all active subscriptions for this user and plan, not just the aiscoo designation.'
                )}
              </p>
            </div>
            {plansQuery.isError && (
              <ErrorState
                className='min-h-24'
                title={t('Loading failed')}
                onRetry={() => {
                  void plansQuery.refetch()
                }}
              />
            )}
            <div className='flex flex-col gap-2 sm:flex-row'>
              <Combobox
                options={plans.map((p) => ({
                  value: String(p.plan.id),
                  label: `${p.plan.title} ($${Number(p.plan.price_amount || 0).toFixed(2)})`,
                }))}
                value={selectedPlanId}
                onValueChange={(v) => v !== null && setSelectedPlanId(v)}
                className='flex-1'
                placeholder={t('Select subscription plan')}
                disabled={creating || loading || plansQuery.isError}
                aria-label={t('Select subscription plan')}
              />
              <Button
                onClick={handleCreate}
                disabled={
                  creating || loading || plansQuery.isError || !selectedPlanId
                }
              >
                <Plus className='mr-1 h-4 w-4' />
                {t('Add subscription')}
              </Button>
            </div>

            {subscriptionsQuery.isError && (
              <ErrorState
                className='min-h-24'
                title={t('Loading failed')}
                onRetry={() => {
                  void subscriptionsQuery.refetch()
                }}
              />
            )}
            {!subscriptionsQuery.isError && (
              <StaticDataTable
                data={loading ? [] : subs}
                getRowKey={(record) => record.subscription.id}
                emptyClassName={loading ? 'py-8' : 'text-muted-foreground py-8'}
                emptyContent={
                  loading ? t('Loading...') : t('No subscription records')
                }
                columns={[
                  {
                    id: 'id',
                    header: t('ID'),
                    cell: (record) => (
                      <TableId value={record.subscription.id} />
                    ),
                  },
                  {
                    id: 'plan',
                    header: t('Plan'),
                    cell: (record) => {
                      const sub = record.subscription

                      return (
                        <div>
                          <div className='font-medium'>
                            {planTitleMap.get(sub.plan_id) || `#${sub.plan_id}`}
                          </div>
                          <div className='text-muted-foreground text-sm'>
                            {t('Source')}: {sub.source || '-'}
                            {sub.usage_scope === 'xcode' && (
                              <p>{t('aiscoo client-only subscription')}</p>
                            )}
                          </div>
                          {sub.usage_scope === 'xcode' && (
                            <p className='text-muted-foreground mt-1 text-xs'>
                              {t(
                                'aiscoo subscription history cannot be deleted here. Cancel or revoke it instead; the server checks all references.'
                              )}
                            </p>
                          )}
                        </div>
                      )
                    },
                  },
                  {
                    id: 'status',
                    header: t('Status'),
                    cell: (record) => (
                      <SubscriptionStatusBadge
                        sub={record.subscription}
                        t={t}
                      />
                    ),
                  },
                  {
                    id: 'validity',
                    header: t('Validity'),
                    cell: (record) => {
                      const sub = record.subscription

                      return (
                        <div className='text-sm'>
                          <div>
                            {t('Start')}: {formatTimestamp(sub.start_time)}
                          </div>
                          <div>
                            {t('End')}: {formatTimestamp(sub.end_time)}
                          </div>
                        </div>
                      )
                    },
                  },
                  {
                    id: 'quota',
                    header: t('Total Quota'),
                    cell: (record) => {
                      const sub = record.subscription
                      const total = Number(sub.amount_total || 0)
                      const used = Number(sub.amount_used || 0)
                      return total > 0
                        ? `${formatQuota(used)}/${formatQuota(total)}`
                        : t('Unlimited')
                    },
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    className: 'text-right',
                    cellClassName: 'text-right',
                    cell: (record) => {
                      const sub = record.subscription
                      const now = Date.now() / 1000
                      const isExpired =
                        (sub.end_time || 0) > 0 && sub.end_time < now
                      const isActive = sub.status === 'active' && !isExpired

                      return (
                        <DataTableRowActionMenu ariaLabel={t('Actions')}>
                          <DropdownMenuItem
                            disabled={!isActive}
                            onClick={() => {
                              setAdvanceResetTime(true)
                              setResetAction({
                                planId: sub.plan_id,
                                planTitle:
                                  planTitleMap.get(sub.plan_id) ||
                                  `#${sub.plan_id}`,
                              })
                            }}
                          >
                            {t('Reset quota')}
                            <DropdownMenuShortcut>
                              <RotateCcw size={16} />
                            </DropdownMenuShortcut>
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            disabled={!isActive}
                            onClick={() =>
                              setConfirmAction({
                                type: 'invalidate',
                                subId: sub.id,
                              })
                            }
                          >
                            {t('Invalidate')}
                            <DropdownMenuShortcut>
                              <Ban size={16} />
                            </DropdownMenuShortcut>
                          </DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            variant='destructive'
                            disabled={confirming || sub.usage_scope === 'xcode'}
                            onClick={() =>
                              setConfirmAction({
                                type: 'delete',
                                subId: sub.id,
                              })
                            }
                          >
                            {t('Delete')}
                            <DropdownMenuShortcut>
                              <Trash2 size={16} />
                            </DropdownMenuShortcut>
                          </DropdownMenuItem>
                        </DataTableRowActionMenu>
                      )
                    },
                  },
                ]}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>

      {confirmAction && (
        <ConfirmDialog
          open
          onOpenChange={(v) => !v && !confirming && setConfirmAction(null)}
          title={
            confirmAction.type === 'invalidate'
              ? t('Confirm invalidate')
              : t('Confirm delete')
          }
          desc={
            confirmAction.type === 'invalidate'
              ? t(
                  'After invalidating, this subscription will be immediately deactivated. Historical records are not affected. Continue?'
                )
              : t(
                  'Deleting will permanently remove this subscription record (including benefit details). Continue?'
                )
          }
          handleConfirm={handleConfirmAction}
          destructive={confirmAction.type === 'delete'}
          isLoading={confirming}
        />
      )}

      {resetAction && (
        <ConfirmDialog
          open
          onOpenChange={(v) => !v && !resetting && setResetAction(null)}
          title={t('Reset subscription quota')}
          desc={t('Reset active {{plan}} subscriptions for this user?', {
            plan: resetAction.planTitle,
          })}
          confirmText={t('Reset quota')}
          handleConfirm={handleResetConfirm}
          isLoading={resetting}
        >
          <label className='flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm'>
            <span>{t('Advance next reset time')}</span>
            <Switch
              checked={advanceResetTime}
              onCheckedChange={(checked) => setAdvanceResetTime(!!checked)}
              aria-label={t('Advance next reset time')}
              disabled={resetting}
            />
          </label>
        </ConfirmDialog>
      )}
    </>
  )
}
