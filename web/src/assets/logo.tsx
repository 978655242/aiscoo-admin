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
import type { SVGProps } from 'react'

import { cn } from '@/lib/utils'

export function Logo({ className, ...props }: SVGProps<SVGSVGElement>) {
  return (
    <svg
      id='aiscoo-logo'
      viewBox='0 0 172 172'
      xmlns='http://www.w3.org/2000/svg'
      height='24'
      width='24'
      fill='none'
      stroke='none'
      strokeWidth='2'
      strokeLinecap='round'
      strokeLinejoin='round'
      className={cn('size-6', className)}
      {...props}
    >
      <title>aiscoo</title>
      <defs>
        <clipPath id='thide'>
          <rect x='-200' y='-200' width='311' height='500' />
        </clipPath>
        <clipPath id='thc'>
          <circle cx='44' cy='58' r='29.5' />
        </clipPath>
      </defs>
      <g transform='translate(-8.213114754098356 50.5)'>
        <g clipPath='url(#thide)'>
          <g transform='translate(-6.13 -45.08) scale(1.6393)'>
            <g className='head' transform='translate(21 0)'>
              <g className='tilt'>
                <circle cx='44' cy='58' r='30.5' fill='currentColor' />
                <g clipPath='url(#thc)'>
                  <g className='eyes'>
                    <ellipse
                      className='eye'
                      cx='31.5'
                      cy='57'
                      rx='6.5'
                      ry='9'
                      fill='#FFFFFF'
                    />
                    <ellipse
                      className='eye'
                      cx='47.5'
                      cy='57'
                      rx='6.5'
                      ry='9'
                      fill='#FFFFFF'
                    />
                  </g>
                </g>
              </g>
            </g>
          </g>
        </g>
        <path d='M116 11A11 11 0 0 1 138 11V100H116Z' fill='currentColor' />
        <circle className='idot' cx='127' cy='-18' r='11' fill='currentColor' />
      </g>
    </svg>
  )
}
