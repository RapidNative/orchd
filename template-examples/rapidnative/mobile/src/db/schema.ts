import { defineTable } from '@vibecode-db/client';

export const todos = defineTable('todos', {
  id: 'string',
  title: 'string',
  completed: 'boolean',
  created_at: 'string',
  updated_at: 'string',
});

export type Todo = {
  id: string;
  title: string;
  completed: boolean;
  created_at: string;
  updated_at: string;
};
