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
import { User } from 'lucide-react'
import { useState } from 'react'

import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import {
  getUserAvatarDataUri,
  getUserAvatarSeed,
  type UserAvatarId,
} from '@/lib/avatar'
import { cn } from '@/lib/utils'

interface UserAvatarProps {
  userId: UserAvatarId
  avatarUrl?: string | null
  alt?: string
  anonymous?: boolean
  className?: string
}

export function UserAvatar(props: UserAvatarProps) {
  const seed = props.anonymous ? null : getUserAvatarSeed(props.userId)
  const avatarUrl = seed ? props.avatarUrl?.trim() || undefined : undefined

  return (
    <Avatar
      aria-hidden={props.anonymous || !props.alt ? true : undefined}
      className={cn(
        'overflow-hidden rounded-full after:rounded-[inherit]',
        props.className
      )}
    >
      <UserAvatarImage
        // 身份或来源变化时同步重置失败状态，防止展示上一个账户的图片。
        key={JSON.stringify([seed, avatarUrl])}
        userId={seed ? props.userId : null}
        avatarUrl={avatarUrl}
        alt={props.anonymous ? '' : props.alt || ''}
      />
      <AvatarFallback className='rounded-[inherit]'>
        <User className='size-1/2' aria-hidden='true' />
      </AvatarFallback>
    </Avatar>
  )
}

function UserAvatarImage(props: {
  userId: UserAvatarId
  avatarUrl?: string
  alt: string
}) {
  const [failedImage, setFailedImage] = useState<'real' | 'generated' | null>(
    null
  )
  if (failedImage === 'generated') return null

  const isRealImage = Boolean(props.avatarUrl && !failedImage)
  const src = isRealImage ? props.avatarUrl : getUserAvatarDataUri(props.userId)
  if (!src) return null

  return (
    <AvatarImage
      key={src}
      src={src}
      alt={props.alt}
      className='rounded-[inherit]'
      onError={() => setFailedImage(isRealImage ? 'real' : 'generated')}
      onLoadingStatusChange={(status) => {
        if (status === 'error') {
          setFailedImage(isRealImage ? 'real' : 'generated')
        }
      }}
    />
  )
}
