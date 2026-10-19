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
// XCODE: Apply display-only Native preferences before CSS and React; never expose a bridge or identity.
;(function () {
  if (location.pathname !== '/xcode/plans') return
  let context
  try {
    const cookie = document.cookie
      .split(';')
      .map((value) => value.trim())
      .find((value) => value.startsWith('xcode_plan_display_v1='))
    if (!cookie) return
    context = JSON.parse(
      decodeURIComponent(cookie.slice('xcode_plan_display_v1='.length))
    )
  } catch {
    return
  }
  if (
    !context ||
    typeof context !== 'object' ||
    Array.isArray(context) ||
    Object.keys(context).length !== 5 ||
    !Object.keys(context).every(
      (key) =>
        key === 'version' ||
        key === 'theme' ||
        key === 'locale' ||
        key === 'initialAudience' ||
        key === 'uiFontSizePx'
    ) ||
    context.version !== 1 ||
    (context.theme !== 'light' && context.theme !== 'dark') ||
    (context.locale !== 'zh-CN' && context.locale !== 'en-US') ||
    (context.initialAudience !== 'personal' &&
      context.initialAudience !== 'team') ||
    !Number.isInteger(context.uiFontSizePx) ||
    context.uiFontSizePx < 12 ||
    context.uiFontSizePx > 20
  ) {
    return
  }

  const root = document.documentElement
  root.classList.remove('light', 'dark')
  root.classList.add(context.theme)
  root.lang = context.locale
  root.style.colorScheme = context.theme
  root.style.setProperty('--ui-font-size', `${context.uiFontSizePx}px`)
  root.dataset.xcodePlanAudience = context.initialAudience
  root.dataset.xcodePlanTheme = context.theme
  root.dataset.xcodePlanLocale = context.locale
  root.dataset.xcodePlanFontSize = String(context.uiFontSizePx)
  try {
    localStorage.setItem('newapi:theme:v1:mode', context.theme)
    localStorage.setItem(
      'i18nextLng',
      context.locale === 'zh-CN' ? 'zhCN' : 'en'
    )
  } catch {
    // Storage may be unavailable; ThemeProvider/i18n consume the validated DOM instead.
  }
})()
