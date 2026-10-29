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
import { act, fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { UserAvatar } from '..'

beforeEach(() => {
  // Base UI 通过原生 Image 预加载；jsdom 不加载图片，只控制浏览器加载边界。
  vi.spyOn(HTMLImageElement.prototype, 'complete', 'get').mockReturnValue(true)
  vi.spyOn(HTMLImageElement.prototype, 'naturalWidth', 'get').mockReturnValue(
    231
  )
})

describe('user avatar image fallback', () => {
  it.each([undefined, null, '', '   '])(
    'uses a local SVG for absent avatar URL %s',
    (avatarUrl) => {
      const { container } = render(
        <UserAvatar userId={42} avatarUrl={avatarUrl} alt='Current user' />
      )
      expect(screen.getByRole('img', { name: 'Current user' })).toHaveAttribute(
        'src',
        expect.stringMatching(/^data:image\/svg\+xml;charset=utf-8,/)
      )
      expect(container.querySelector('svg path')).toBeNull()
    }
  )

  it('keeps a real avatar while it loads and switches to Multiavatar after a load failure', () => {
    let pending: HTMLImageElement | undefined
    vi.spyOn(window, 'Image').mockImplementation(function () {
      pending = document.createElement('img')
      Object.defineProperty(pending, 'complete', { value: false })
      return pending
    })
    const { container } = render(
      <UserAvatar
        userId={42}
        avatarUrl='/profile-photo.png'
        alt='Current user'
      />
    )
    expect(pending?.getAttribute('src')).toBe('/profile-photo.png')
    expect(container.querySelector('img[src^="data:"]')).toBeNull()
    act(() => {
      if (!pending) throw new Error('Missing image request')
      fireEvent.error(pending)
    })
    expect(pending?.getAttribute('src')).toMatch(/^data:image\/svg\+xml/)
    act(() => {
      if (!pending) throw new Error('Missing fallback image request')
      fireEvent.load(pending)
    })
    expect(screen.getByRole('img', { name: 'Current user' })).toHaveAttribute(
      'src',
      expect.stringMatching(/^data:image\/svg\+xml/)
    )
  })

  it('uses a real avatar first and falls back through generated image failure to the neutral icon', () => {
    const { container } = render(
      <UserAvatar
        userId={42}
        avatarUrl='/profile-photo.png'
        alt='Current user'
      />
    )
    expect(screen.getByRole('img', { name: 'Current user' })).toHaveAttribute(
      'src',
      '/profile-photo.png'
    )
    fireEvent.error(screen.getByRole('img', { name: 'Current user' }))
    expect(screen.getByRole('img', { name: 'Current user' })).toHaveAttribute(
      'src',
      expect.stringMatching(/^data:image\/svg\+xml/)
    )
    fireEvent.error(screen.getByRole('img', { name: 'Current user' }))
    expect(container.querySelector('img')).toBeNull()
    expect(
      container.querySelector('[data-slot="avatar-fallback"] svg')
    ).not.toBeNull()
  })

  it('resets failed image state when the URL or user changes', () => {
    const { rerender } = render(
      <UserAvatar userId={42} avatarUrl='/photo-a.png' alt='Current user' />
    )
    fireEvent.error(screen.getByRole('img', { name: 'Current user' }))
    const generated = screen
      .getByRole('img', { name: 'Current user' })
      .getAttribute('src')
    rerender(
      <UserAvatar userId={42} avatarUrl='/photo-b.png' alt='Current user' />
    )
    expect(screen.getByRole('img', { name: 'Current user' })).toHaveAttribute(
      'src',
      '/photo-b.png'
    )
    rerender(<UserAvatar userId={43} alt='Next user' />)
    expect(screen.getByRole('img', { name: 'Next user' })).not.toHaveAttribute(
      'src',
      generated
    )
    expect(
      screen.queryByRole('img', { name: 'Current user' })
    ).not.toBeInTheDocument()
  })

  it('uses the neutral icon when a generated image cannot be decoded', () => {
    vi.spyOn(HTMLImageElement.prototype, 'naturalWidth', 'get').mockReturnValue(
      0
    )
    const { container } = render(<UserAvatar userId={42} alt='Current user' />)
    expect(container.querySelector('img')).toBeNull()
    expect(
      container.querySelector('[data-slot="avatar-fallback"] svg')
    ).not.toBeNull()
  })

  it('does not load an avatar for an invalid identity', () => {
    const image = vi.spyOn(window, 'Image')
    const { container } = render(
      <UserAvatar userId='042' avatarUrl='/photo.png' alt='Current user' />
    )
    expect(container.querySelector('img')).toBeNull()
    expect(
      container.querySelector('[data-slot="avatar-fallback"] svg')
    ).not.toBeNull()
    expect(image).not.toHaveBeenCalled()
  })

  it('removes image identity and accessible names immediately when anonymous', () => {
    const { container, rerender } = render(
      <UserAvatar userId={42} avatarUrl='/photo.png' alt='Private user' />
    )
    expect(screen.getByRole('img', { name: 'Private user' })).toBeVisible()
    rerender(
      <UserAvatar
        userId={42}
        avatarUrl='/photo.png'
        alt='Private user'
        anonymous
      />
    )
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('[data-slot="avatar"]')).toHaveAttribute(
      'aria-hidden',
      'true'
    )
    expect(container.innerHTML).not.toContain('Private user')
    expect(container.innerHTML).not.toContain('/photo.png')
    rerender(<UserAvatar userId={43} alt='Next user' />)
    expect(screen.getByRole('img', { name: 'Next user' })).toHaveAttribute(
      'src',
      expect.stringMatching(/^data:image\/svg\+xml/)
    )
  })
})
