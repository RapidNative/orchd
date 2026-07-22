import todos from './todos';

export type SeedEntry<T = Record<string, unknown>> = { table: string; rows: T[] };
export const seeds: SeedEntry[] = [todos];
