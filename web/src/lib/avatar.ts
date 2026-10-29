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
import multiavatar from '@multiavatar/multiavatar/esm'

export type UserAvatarId = string | number | null | undefined

const avatarCache = new Map<string, string>()
const avatarCacheLimit = 128

export function getUserAvatarSeed(userId: UserAvatarId): string | null {
  if (typeof userId === 'number') {
    if (!Number.isSafeInteger(userId) || userId <= 0) return null
    return `aiscoo:new-api:multiavatar:v1:${userId}`
  }
  if (
    typeof userId !== 'string' ||
    userId !== userId.trim() ||
    !/^[1-9][0-9]*$/.test(userId)
  ) {
    return null
  }
  return `aiscoo:new-api:multiavatar:v1:${userId}`
}

export function getUserAvatarDataUri(userId: UserAvatarId): string | null {
  const seed = getUserAvatarSeed(userId)
  if (!seed) return null
  const cached = avatarCache.get(seed)
  if (cached) return cached

  try {
    // 两端固定使用 1.0.7 的默认背景与版本参数，避免同一用户跨端头像漂移。
    const svg = multiavatar(seed)
    const uri = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
    if (avatarCache.size >= avatarCacheLimit) {
      const oldestSeed = avatarCache.keys().next().value
      if (oldestSeed) avatarCache.delete(oldestSeed)
    }
    avatarCache.set(seed, uri)
    return uri
  } catch {
    return null
  }
}
