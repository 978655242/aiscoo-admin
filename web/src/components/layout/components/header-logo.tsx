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
import { useState } from 'react'

import { DEFAULT_LOGO } from '@/lib/constants'
import { cn } from '@/lib/utils'

interface HeaderLogoProps {
  src: string
  alt?: string
  loading: boolean
  logoLoaded: boolean
  className?: string
  wordmark?: boolean
  animated?: boolean
}

/**
 * Logo component for header with loading state
 * Shows image only when fully loaded for smooth UX
 */
export function HeaderLogo({
  src,
  alt = 'logo',
  loading,
  logoLoaded,
  className,
  wordmark = false,
  animated = false,
}: HeaderLogoProps) {
  const [failedSource, setFailedSource] = useState<string | null>(null)
  const defaultBrand = src === DEFAULT_LOGO || src === failedSource
  if (defaultBrand) {
    const asset = wordmark ? 'wordmark' : 'mark'
    return (
      <span
        className={cn('inline-grid shrink-0', className)}
        role='img'
        aria-label={alt}
      >
        <picture className='col-start-1 row-start-1 block dark:hidden'>
          {wordmark && animated && (
            <source
              media='(prefers-reduced-motion: reduce)'
              srcSet='/brand/wordmark-light.svg'
            />
          )}
          <img
            src={`/brand/${wordmark && animated ? 'wordmark-animated' : asset}-light.svg`}
            alt=''
            className='size-full object-contain'
          />
        </picture>
        <picture className='col-start-1 row-start-1 hidden dark:block'>
          {wordmark && animated && (
            <source
              media='(prefers-reduced-motion: reduce)'
              srcSet='/brand/wordmark-dark.svg'
            />
          )}
          <img
            src={`/brand/${wordmark && animated ? 'wordmark-animated' : asset}-dark.svg`}
            alt=''
            className='size-full object-contain'
          />
        </picture>
      </span>
    )
  }
  return (
    <img
      src={src}
      alt={alt}
      onError={() => setFailedSource(src)}
      className={cn(
        'h-6 w-6 rounded-lg object-contain transition-opacity duration-200',
        !loading && logoLoaded ? 'opacity-100' : 'opacity-0',
        className
      )}
    />
  )
}
