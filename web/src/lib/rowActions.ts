import { PencilSquareIcon, TrashIcon } from '@heroicons/vue/20/solid'
import type { RowAction } from '../components/ui/RowActions.vue'

/** The edit and delete buttons that end a row of a manageable list. */
export function editDeleteActions(editLabel: string, deleteLabel: string): RowAction[] {
  return [
    { key: 'edit', label: editLabel, icon: PencilSquareIcon },
    { key: 'delete', label: deleteLabel, icon: TrashIcon, tone: 'danger' },
  ]
}
