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
import { DEFAULT_LOGO, DEFAULT_SYSTEM_NAME } from '@/lib/constants'

export function normalizeSystemName(name: unknown): string {
  if (typeof name !== 'string') return DEFAULT_SYSTEM_NAME
  switch (name.trim().toLowerCase()) {
    case '':
    case 'new api':
    case 'newapi':
    case 'new-api':
    case 'one api':
    case 'oneapi':
    case 'one-api':
    case 'xwork':
    case 'xcode':
    case 'aiscoo':
      return DEFAULT_SYSTEM_NAME
    default:
      return name
  }
}

export function normalizeSystemLogo(logo: unknown): string {
  if (typeof logo !== 'string') return DEFAULT_LOGO
  switch (logo.trim()) {
    case '':
    case '/logo.png':
    case '/favicon.ico':
      return DEFAULT_LOGO
    default:
      return logo
  }
}
