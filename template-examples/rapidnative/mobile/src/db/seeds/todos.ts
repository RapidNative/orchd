import type { Todo } from '../schema';
import type { SeedEntry } from './index';

const seed: SeedEntry<Todo> = {
  table: 'todos',
  rows: [
    { id: "todo-1", title: "Buy groceries", completed: false, created_at: "2026-07-20T13:10:59.835Z", updated_at: "2026-07-20T13:10:59.835Z" },
    { id: "todo-2", title: "Walk the dog", completed: true, created_at: "2026-07-20T13:10:59.835Z", updated_at: "2026-07-20T13:10:59.835Z" },
    { id: "todo-3", title: "Finish the report", completed: false, created_at: "2026-07-20T13:10:59.835Z", updated_at: "2026-07-20T13:10:59.835Z" },
    { id: "todo-4", title: "Call mom", completed: false, created_at: "2026-07-20T13:10:59.835Z", updated_at: "2026-07-20T13:10:59.835Z" },
    { id: "todo-5", title: "Schedule dentist appointment", completed: true, created_at: "2026-07-20T13:10:59.835Z", updated_at: "2026-07-20T13:10:59.835Z" },
  ],
};

export default seed;
