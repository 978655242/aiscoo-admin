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
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { createContext, Script } from 'node:vm'

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { createElement } from 'react'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest'

import { ThemeProvider, useTheme } from '@/context/theme-provider'

import { readXCodePlanDisplayContext } from '../display-context'

// XCODE: Execute the shipped head script against real DOM/Storage before React starts.
let bootstrap: Script
beforeAll(async () => {
  bootstrap = new Script(
    await readFile(
      resolve(process.cwd(), 'public/xcode-plan-display.js'),
      'utf8'
    ),
    { filename: 'xcode-plan-display.js' }
  )
})
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('newapi:theme:v1:mode', 'dark')
  localStorage.setItem('i18nextLng', 'en')
  localStorage.setItem('native-admin-setting', 'keep')
})
const originalLocation = window.location.href
const originalRoot = {
  className: document.documentElement.className,
  lang: document.documentElement.lang,
  style: document.documentElement.style.cssText,
}
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  document.cookie = 'xcode_plan_display_v1=; Path=/xcode/plans; Max-Age=0'
  window.history.replaceState({}, '', originalLocation)
  document.documentElement.className = originalRoot.className
  document.documentElement.lang = originalRoot.lang
  document.documentElement.style.cssText = originalRoot.style
  delete document.documentElement.dataset.xcodePlanAudience
  delete document.documentElement.dataset.xcodePlanTheme
  delete document.documentElement.dataset.xcodePlanLocale
  delete document.documentElement.dataset.xcodePlanFontSize
})

const display = {
  version: 1,
  theme: 'light',
  locale: 'zh-CN',
  initialAudience: 'team',
  uiFontSizePx: 14,
}
const cookieFor = (value: unknown) =>
  `xcode_plan_display_v1=${encodeURIComponent(JSON.stringify(value))}`

function viewer(path = '/xcode/plans', cookie = cookieFor(display)) {
  const dom = document.implementation.createHTMLDocument('Native viewer')
  dom.documentElement.className = 'dark existing-class'
  dom.documentElement.lang = 'en-US'
  dom.documentElement.style.cssText =
    'font-size:16px;color-scheme:dark;--ui-font-size:14px'
  const readCookie = vi.fn(() => cookie)
  Object.defineProperty(dom, 'cookie', { configurable: true, get: readCookie })
  const context = createContext({
    document: dom,
    location: new URL(path, 'https://native.example'),
    localStorage,
    navigator: { language: 'en-US', languages: ['en-US'] },
    matchMedia: () => ({ matches: true }),
  })
  context.window = context
  context.self = context
  return { dom, context, readCookie }
}

function firstFrame(dom: Document) {
  const html = dom.documentElement
  return {
    dark: html.classList.contains('dark'),
    light: html.classList.contains('light'),
    existingClass: html.classList.contains('existing-class'),
    lang: html.lang,
    colorScheme: html.style.colorScheme,
    fontSize: html.style.fontSize,
    uiFontSize: html.style.getPropertyValue('--ui-font-size'),
    datasets: { ...html.dataset },
  }
}
const defaultFrame = {
  dark: true,
  light: false,
  existingClass: true,
  lang: 'en-US',
  colorScheme: 'dark',
  fontSize: '16px',
  uiFontSize: '14px',
  datasets: {},
}

function expectNativeFrame(dom: Document, fontSize = 14) {
  expect(firstFrame(dom)).toEqual({
    dark: false,
    light: true,
    existingClass: true,
    lang: 'zh-CN',
    colorScheme: 'light',
    fontSize: '16px',
    uiFontSize: `${fontSize}px`,
    datasets: {
      xcodePlanAudience: 'team',
      xcodePlanTheme: 'light',
      xcodePlanLocale: 'zh-CN',
      xcodePlanFontSize: String(fontSize),
    },
  })
}

function applyNativeBootstrapToPage() {
  window.history.replaceState({}, '', '/xcode/plans')
  document.cookie = `${cookieFor(display)}; Path=/xcode/plans`
  document.documentElement.className = 'dark existing-class'
  document.documentElement.lang = 'en-US'
  document.documentElement.style.cssText =
    'font-size:16px;color-scheme:dark;--ui-font-size:14px'
  bootstrap.runInContext(
    createContext({ document, location: window.location, localStorage })
  )
}

function ThemeReadback() {
  return createElement(
    'output',
    { 'aria-label': 'Resolved theme' },
    useTheme().resolvedTheme
  )
}

describe('XCode head display bootstrap', () => {
  it.each([
    '/xcode/plans',
    '/xcode/plans?handoff=short-code',
    '/xcode/plans#details',
  ])(
    'sets the first frame synchronously on exact viewer %s despite dark/English defaults',
    (path) => {
      const fixture = viewer(
        path,
        `unrelated=keep; ${cookieFor(display)}; xcode_plan=not-ui-data`
      )
      bootstrap.runInContext(fixture.context)
      expectNativeFrame(fixture.dom)
      expect(localStorage.getItem('newapi:theme:v1:mode')).toBe('light')
      expect(localStorage.getItem('i18nextLng')).toBe('zhCN')
      expect(localStorage.getItem('native-admin-setting')).toBe('keep')
      expect(fixture.context.xcodePlanDisplay).toBeUndefined()
      expect(fixture.context.zcode).toBeUndefined()
    }
  )

  it('preserves a valid personal/dark/English entry without inventing team state', () => {
    const fixture = viewer(
      '/xcode/plans',
      cookieFor({
        version: 1,
        theme: 'dark',
        locale: 'en-US',
        initialAudience: 'personal',
        uiFontSizePx: 14,
      })
    )
    bootstrap.runInContext(fixture.context)
    expect(fixture.dom.documentElement.dataset).toMatchObject({
      xcodePlanAudience: 'personal',
      xcodePlanTheme: 'dark',
      xcodePlanLocale: 'en-US',
      xcodePlanFontSize: '14',
    })
    expect(fixture.dom.documentElement.classList.contains('dark')).toBe(true)
    expect(fixture.dom.documentElement.lang).toBe('en-US')
    expect(localStorage.getItem('i18nextLng')).toBe('en')
  })

  it.each([12, 14, 20])(
    'applies %ipx UI text without changing rem sizing',
    (uiFontSizePx) => {
      const fixture = viewer(
        '/xcode/plans',
        cookieFor({ ...display, uiFontSizePx })
      )
      bootstrap.runInContext(fixture.context)
      expectNativeFrame(fixture.dom, uiFontSizePx)
    }
  )

  it.each(['getter', 'getItem', 'setItem'])(
    'still exposes the validated DOM first frame when storage %s is unavailable',
    (failure) => {
      const fixture = viewer()
      const unavailable = () => {
        throw new DOMException('Storage blocked', 'SecurityError')
      }
      if (failure === 'getter') {
        Object.defineProperty(fixture.context, 'localStorage', {
          get: unavailable,
        })
      } else {
        vi.spyOn(Storage.prototype, failure).mockImplementation(unavailable)
      }
      expect(() => bootstrap.runInContext(fixture.context)).not.toThrow()
      expectNativeFrame(fixture.dom)
    }
  )

  it.each([
    ['missing cookie', ''],
    [
      'wrong cookie name',
      `xcode_plan_display_v10=${encodeURIComponent(JSON.stringify(display))}`,
    ],
    ['invalid percent escape', 'xcode_plan_display_v1=%E0%A4%A'],
    ['invalid JSON', 'xcode_plan_display_v1=%7B'],
    ['null context', cookieFor(null)],
    ['array context', cookieFor([display])],
    ['future version', cookieFor({ ...display, version: 2 })],
    [
      'missing version',
      cookieFor({
        theme: 'light',
        locale: 'zh-CN',
        initialAudience: 'team',
        uiFontSizePx: 14,
      }),
    ],
    ['system theme', cookieFor({ ...display, theme: 'system' })],
    ['unsupported locale', cookieFor({ ...display, locale: 'zhCN' })],
    [
      'unknown audience',
      cookieFor({ ...display, initialAudience: 'enterprise' }),
    ],
    ['undersized font', cookieFor({ ...display, uiFontSizePx: 11 })],
    ['oversized font', cookieFor({ ...display, uiFontSizePx: 21 })],
    ['fractional font', cookieFor({ ...display, uiFontSizePx: 14.5 })],
    ['string font', cookieFor({ ...display, uiFontSizePx: '14' })],
    ['unknown identity key', cookieFor({ ...display, sid: 'not-an-identity' })],
    ['unknown permission key', cookieFor({ ...display, canPurchase: true })],
    [
      'unknown URL key',
      cookieFor({ ...display, url: 'https://foreign.example' }),
    ],
  ])(
    'ignores the whole %s context without partially changing defaults',
    (_name, cookie) => {
      const fixture = viewer('/xcode/plans', cookie)
      const getStorage = vi.spyOn(Storage.prototype, 'getItem')
      const setStorage = vi.spyOn(Storage.prototype, 'setItem')
      expect(() => bootstrap.runInContext(fixture.context)).not.toThrow()
      expect(firstFrame(fixture.dom)).toEqual(defaultFrame)
      expect(getStorage).not.toHaveBeenCalled()
      expect(setStorage).not.toHaveBeenCalled()
    }
  )

  it.each([
    '/',
    '/dashboard',
    '/xcode/authorize',
    '/xcode/plans/',
    '/xcode/plans/other',
    '/xcode/%70lans',
    '/other/xcode/plans',
  ])(
    'does not read cookies or touch native admin DOM/storage on %s',
    (path) => {
      const fixture = viewer(path)
      const getStorage = vi.spyOn(Storage.prototype, 'getItem')
      const setStorage = vi.spyOn(Storage.prototype, 'setItem')
      bootstrap.runInContext(fixture.context)
      expect(firstFrame(fixture.dom)).toEqual(defaultFrame)
      expect(fixture.readCookie).not.toHaveBeenCalled()
      expect(getStorage).not.toHaveBeenCalled()
      expect(setStorage).not.toHaveBeenCalled()
    }
  )

  it('keeps the validated Native theme after ThemeProvider mounts with blocked Storage and a dark system', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('Storage blocked', 'SecurityError')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('Storage blocked', 'SecurityError')
    })
    const matchMedia = window.matchMedia.bind(window)
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...matchMedia(query),
      matches: true,
    }))
    applyNativeBootstrapToPage()
    render(createElement(ThemeProvider, null, createElement(ThemeReadback)))
    expect(screen.getByLabelText('Resolved theme')).toHaveTextContent('light')
    expectNativeFrame(document)
  })

  it('initializes actual i18n from Native Chinese DOM when English Storage cannot be read or written', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('Storage blocked', 'SecurityError')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('Storage blocked', 'SecurityError')
    })
    applyNativeBootstrapToPage()
    vi.resetModules()
    // XCODE: Test the module-load boundary; a static import runs before the head DOM projection.
    const { default: i18n } = await import('@/i18n/config')
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('zhCN'))
    expectNativeFrame(document)
  })

  it('rejects incomplete or altered DOM projections before React/providers consume them', () => {
    applyNativeBootstrapToPage()
    expect(readXCodePlanDisplayContext()).toEqual({
      theme: 'light',
      locale: 'zh-CN',
      initialAudience: 'team',
      uiFontSizePx: 14,
    })
    document.documentElement.dataset.xcodePlanFontSize = '14.5'
    expect(readXCodePlanDisplayContext()).toBeNull()
    document.documentElement.dataset.xcodePlanFontSize = '14'
    delete document.documentElement.dataset.xcodePlanLocale
    expect(readXCodePlanDisplayContext()).toBeNull()
    document.documentElement.dataset.xcodePlanLocale = 'zh-CN'
    window.history.replaceState({}, '', '/dashboard')
    expect(readXCodePlanDisplayContext()).toBeNull()
  })
})
