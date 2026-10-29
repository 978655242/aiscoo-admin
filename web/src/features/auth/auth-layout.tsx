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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { useSystemConfig } from '@/hooks/use-system-config'
import { cn } from '@/lib/utils'

type AuthLayoutProps = {
  children: React.ReactNode
  /** Rendered in the card's tinted header band. */
  title?: React.ReactNode
  description?: React.ReactNode
  /** Wider card for content-heavy pages such as application authorization. */
  wide?: boolean
  dualMethods?: boolean
}

export function AuthLayout(props: AuthLayoutProps) {
  const { t } = useTranslation()
  const { systemName, logo, loading } = useSystemConfig()
  const framed = window.self !== window.top
  const showHeader = !framed && props.title != null

  return (
    <div className='auth-shell bg-background text-foreground flex min-h-svh flex-col'>
      <header className='px-4 pt-5 sm:px-8 sm:pt-6'>
        <Link
          to='/'
          className='inline-flex items-center gap-2 transition-opacity hover:opacity-80'
        >
          <span className='relative size-7'>
            {loading ? (
              <Skeleton className='absolute inset-0 rounded-full' />
            ) : (
              <img
                src={logo}
                alt={t('Logo')}
                className='size-7 rounded-full object-cover'
              />
            )}
          </span>
          {loading ? (
            <Skeleton className='h-6 w-24' />
          ) : (
            <h1 className='text-lg font-semibold tracking-tight'>
              {systemName}
            </h1>
          )}
        </Link>
      </header>

      <main className='flex flex-1 items-start justify-center px-4 py-8 sm:items-center sm:py-10'>
        <div
          className={cn(
            'bg-card w-full overflow-hidden rounded-2xl shadow-[0_8px_32px_rgba(19,76,255,0.06)]',
            props.dualMethods && 'md:max-w-[880px]',
            !props.dualMethods &&
              (props.wide ? 'sm:max-w-[560px]' : 'sm:max-w-[480px]')
          )}
          role={framed ? 'alert' : undefined}
        >
          {showHeader && (
            <div className='auth-shell-band px-6 py-6 sm:px-8 sm:py-7'>
              <h2 className='text-2xl leading-tight font-medium tracking-tight sm:text-[28px]'>
                {props.title}
              </h2>
              {props.description != null && (
                <div className='text-muted-foreground mt-2 text-sm sm:text-base'>
                  {props.description}
                </div>
              )}
            </div>
          )}
          <div className='px-6 py-6 sm:px-8 sm:py-8'>
            {framed ? (
              <ErrorState
                title={t(
                  'Open this page in a browser to sign in or authorize an application.'
                )}
              />
            ) : (
              props.children
            )}
          </div>
        </div>
      </main>

      <footer className='text-muted-foreground/70 px-4 pb-5 text-center text-xs'>
        {!loading && `© ${new Date().getFullYear()} ${systemName}`}
      </footer>
    </div>
  )
}
