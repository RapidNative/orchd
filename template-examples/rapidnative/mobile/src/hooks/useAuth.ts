/**
 * useAuth Hook
 *
 * Authentication hook providing user state and auth actions via React Query.
 */

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useApp } from '@/src/providers/AppProvider';

export interface User {
  id: string;
  email: string;
}

export class AuthError extends Error {
  reason?: string;
  constructor(message: string, reason?: string) {
    super(message);
    this.reason = reason;
  }
}

export const authKeys = {
  session: ['auth', 'session'] as const,
  user: ['auth', 'user'] as const,
};

export function useAuth() {
  const { client } = useApp();
  const queryClient = useQueryClient();

  const sessionQuery = useQuery({
    queryKey: authKeys.session,
    queryFn: async () => {
      const { data, error } = await client.auth.getSession();
      if (error) throw error;
      return data.session;
    },
    staleTime: 0,
  });

  const session = sessionQuery.data ?? null;
  const user: User | null = session?.user
    ? { id: session.user.id, email: session.user.email ?? '' }
    : null;

  const signIn = useMutation({
    mutationFn: async ({ email, password }: { email: string; password: string }) => {
      const { data, error } = await client.auth.signInWithPassword({ email, password });
      if (error) throw new AuthError(error.message, error.reason);
      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.session }),
  });

  const signUp = useMutation({
    mutationFn: async ({ email, password }: { email: string; password: string }) => {
      const { data, error } = await client.auth.signUp({ email, password });
      if (error) throw new AuthError(error.message, error.reason);
      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.session }),
  });

  const signOut = useMutation({
    mutationFn: async () => {
      const { error } = await client.auth.signOut();
      if (error) throw new AuthError(error.message, error.reason);
    },
    onSuccess: () => {
      queryClient.setQueryData(authKeys.session, null);
      queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== 'auth' });
    },
  });

  return {
    user,
    session,
    isAuthenticated: !!session,
    isLoading: sessionQuery.isLoading,
    signIn,
    signUp,
    signOut,
  };
}
