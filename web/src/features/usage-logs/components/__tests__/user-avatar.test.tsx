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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type Table,
} from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import type { TaskLog } from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { useTaskLogsColumns } from '../columns/task-logs-columns'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

const commonLog = usageLogSchema.parse({
  id: 1,
  user_id: 42,
  username: 'current-username',
  created_at: 1,
  type: 2,
  content: '',
})
const taskLog: TaskLog = {
  id: 2,
  user_id: 42,
  username: 'historical-username',
  platform: 'task',
  task_id: 'task-1',
  action: 'GENERATE',
  channel_id: 1,
  group: 'default',
  quota: 0,
  submit_time: 1,
  status: 'SUCCESS',
}

function UserColumn<TData>(props: { table: Table<TData> }) {
  const cell = props.table
    .getRowModel()
    .rows[0].getAllCells()
    .find((entry) => entry.column.id === 'user')
  return cell ? flexRender(cell.column.columnDef.cell, cell.getContext()) : null
}

function UserCells() {
  const context = useUsageLogsContext()
  const commonColumns = useCommonLogsColumns(true, false)
  const taskColumns = useTaskLogsColumns(true, false)
  const common = useReactTable({
    data: [commonLog],
    columns: commonColumns,
    getCoreRowModel: getCoreRowModel(),
  })
  const tasks = useReactTable({
    data: [taskLog],
    columns: taskColumns,
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <>
      <button
        type='button'
        onClick={() => context.setSensitiveVisible(!context.sensitiveVisible)}
      >
        Toggle privacy
      </button>
      <UserColumn table={common} />
      <UserColumn table={tasks} />
      {context.userInfoDialogOpen ? (
        <output>{context.selectedUserId}</output>
      ) : null}
    </>
  )
}

it('uses the same ID avatar for ordinary and task users and removes stable images under privacy', async () => {
  vi.spyOn(HTMLImageElement.prototype, 'complete', 'get').mockReturnValue(true)
  vi.spyOn(HTMLImageElement.prototype, 'naturalWidth', 'get').mockReturnValue(
    231
  )
  const user = userEvent.setup()
  const { container } = render(
    <QueryClientProvider client={new QueryClient()}>
      <UsageLogsProvider>
        <UserCells />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
  const images = container.querySelectorAll('[data-slot="avatar"] img')
  expect(images).toHaveLength(2)
  expect(images[0]).toHaveAttribute(
    'src',
    expect.stringMatching(/^data:image\/svg\+xml/)
  )
  expect(images[1]).toHaveAttribute('src', images[0].getAttribute('src'))

  await user.click(screen.getByRole('button', { name: 'current-username' }))
  expect(screen.getByRole('status')).toHaveTextContent('42')
  await user.click(screen.getByRole('button', { name: 'Toggle privacy' }))
  expect(container.querySelector('[data-slot="avatar"] img')).toBeNull()
  expect(screen.queryByText('current-username')).not.toBeInTheDocument()
  expect(screen.queryByText('historical-username')).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Toggle privacy' }))
  expect(container.querySelectorAll('[data-slot="avatar"] img')).toHaveLength(2)
})
