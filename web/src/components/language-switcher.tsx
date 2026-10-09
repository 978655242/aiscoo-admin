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
import { useCallback } from 'react'

import { LanguageMenu } from '@/components/language-menu'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

export function LanguageSwitcher() {
  const user = useAuthStore((s) => s.auth.user)
  const handleChangeLanguage = useCallback(
    async (code: string) => {
      if (user) {
        try {
          await api.put('/api/user/self', { language: code })
        } catch {
          // Best-effort persistence; don't block the UI on failure
        }
      }
    },
    [user]
  )

  return <LanguageMenu onLanguageChange={handleChangeLanguage} />
}
