import { readonly } from 'vue'
import { useState } from '#app'

export type DialogResult = { status: boolean }

export type DialogConfirmColor =
  | 'primary'
  | 'secondary'
  | 'success'
  | 'info'
  | 'warning'
  | 'error'
  | 'neutral'

export type ConfirmOptions = {
  title?: string
  message?: string
  confirmText?: string
  cancelText?: string
  confirmColor?: DialogConfirmColor
}

type QueueItem = {
  opts: ConfirmOptions
  resolve: (result: DialogResult) => void
}

type DialogState = {
  current: QueueItem | null
  queue: Array<QueueItem>
}

export const useDialog = () => {
  const raw = useState<DialogState>('dialog:state', () => ({
    current: null,
    queue: [],
  }))
  const state = raw.value

  const dequeue = () => {
    if (!state.current) {
      state.current = state.queue.shift() ?? null
    }
  }

  const confirmDialog = (opts: ConfirmOptions): Promise<DialogResult> =>
    new Promise((resolve) => {
      state.queue.push({ opts, resolve })
      dequeue()
    })

  const finish = (status: boolean) => {
    if (!state.current) {
      return
    }

    state.current.resolve({ status })
    state.current = null
    dequeue()
  }

  return {
    confirmDialog,
    confirm: () => finish(true),
    cancel: () => finish(false),
    state: readonly(state),
  }
}
