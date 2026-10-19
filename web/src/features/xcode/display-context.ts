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
export interface XCodePlanDisplayContext {
  theme: 'light' | 'dark'
  locale: 'zh-CN' | 'en-US'
  initialAudience: 'personal' | 'team'
  uiFontSizePx: number
}

// XCODE: DOM is a display-only projection, validated again before providers or React consume it.
export function readXCodePlanDisplayContext(): XCodePlanDisplayContext | null {
  if (
    typeof window === 'undefined' ||
    window.location.pathname !== '/xcode/plans'
  ) {
    return null
  }
  const data = document.documentElement.dataset
  const theme = data.xcodePlanTheme
  const locale = data.xcodePlanLocale
  const initialAudience = data.xcodePlanAudience
  const fontSize = data.xcodePlanFontSize
  if (
    (theme !== 'light' && theme !== 'dark') ||
    (locale !== 'zh-CN' && locale !== 'en-US') ||
    (initialAudience !== 'personal' && initialAudience !== 'team') ||
    typeof fontSize !== 'string' ||
    !/^(1[2-9]|20)$/.test(fontSize)
  ) {
    return null
  }
  return { theme, locale, initialAudience, uiFontSizePx: Number(fontSize) }
}
