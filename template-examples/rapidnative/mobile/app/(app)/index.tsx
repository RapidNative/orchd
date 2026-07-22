import { useState } from 'react';
import { View, Text, FlatList, Pressable, TextInput, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import Constants from 'expo-constants';
import { CheckIcon, PlusIcon, Trash2Icon, SettingsIcon } from 'lucide-react-native';
import { router } from 'expo-router';
import { cssInterop, useColorScheme } from 'nativewind';
import { useApp } from '@/src/hooks';

cssInterop(CheckIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(PlusIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(Trash2Icon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(SettingsIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
console.log("hello")
export default function TodosScreen() {
  const { client } = useApp();
  const queryClient = useQueryClient();
  const { colorScheme } = useColorScheme();
  const isDark = colorScheme === 'dark';
  const appName = Constants.expoConfig?.name ?? 'Todos';
  const [newTitle, setNewTitle] = useState('');

  const { data: todos, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['todos'],
    queryFn: async () => {
      const { data, error } = await client
        .from('todos')
        .select('*')
        .order('created_at', { ascending: true });
      if (error) throw error;
      return data ?? [];
    },
  });

  const addTodo = useMutation({
    mutationFn: async (title: string) => {
      const now = new Date().toISOString();
      const { error } = await client.from('todos').insert({
        id: crypto.randomUUID(),
        title,
        completed: false,
        created_at: now,
        updated_at: now,
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['todos'] });
      setNewTitle('');
    },
  });

  const toggleTodo = useMutation({
    mutationFn: async ({ id, completed }: { id: string; completed: boolean }) => {
      const { error } = await client
        .from('todos')
        .update({ completed, updated_at: new Date().toISOString() })
        .eq('id', id);
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['todos'] }),
  });

  const deleteTodo = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await client.from('todos').delete().eq('id', id);
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['todos'] }),
  });

  const handleAdd = () => {
    const trimmed = newTitle.trim();
    if (!trimmed || addTodo.isPending) return;
    addTodo.mutate(trimmed);
  };

  const completedCount = todos?.filter((todo) => todo.completed).length ?? 0;
  const totalCount = todos?.length ?? 0;

  return (
    <SafeAreaView edges={['top']} className="flex-1 bg-background">
      <FlatList
        data={todos}
        keyExtractor={(item) => String(item.id)}
        contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 120 }}
        ListHeaderComponent={
          <View className="pt-6 pb-6 gap-1">
            <Text className="text-foreground text-3xl font-bold tracking-tight">
              {appName}
            </Text>
            <Pressable
              onPress={() => router.push('/settings')}
              className="absolute right-0 top-6"
            >
              <SettingsIcon size={22} className="text-muted-foreground" />
            </Pressable>
            {totalCount > 0 && (
              <Text className="text-muted-foreground text-sm">
                {completedCount} of {totalCount} completed
              </Text>
            )}
          </View>
        }
        ListEmptyComponent={
          <View className="items-center py-16 gap-2">
            <View className="w-16 h-16 rounded-full bg-muted items-center justify-center mb-2">
              <CheckIcon size={28} className="text-muted-foreground" />
            </View>
            <Text className="text-foreground text-lg font-semibold">No tasks yet</Text>
            <Text className="text-muted-foreground text-sm text-center">
              Add your first todo to get started
            </Text>
          </View>
        }
        refreshControl={
          <RefreshControl
            refreshing={isFetching && !isLoading}
            onRefresh={refetch}
            tintColor={isDark ? '#d4d4d4' : '#78716c'}
          />
        }
        renderItem={({ item }) => (
          <Pressable
            onPress={() => toggleTodo.mutate({ id: item.id, completed: !item.completed })}
            className="flex-row items-center gap-3 py-4 active:opacity-60"
          >
            <View
              className={`w-6 h-6 rounded-full border-2 items-center justify-center ${
                item.completed
                  ? 'bg-primary border-primary'
                  : 'border-muted-foreground/30'
              }`}
            >
              {item.completed && <CheckIcon size={14} className="text-primary-foreground" />}
            </View>
            <Text
              numberOfLines={2}
              className={`flex-1 text-base ${
                item.completed
                  ? 'text-muted-foreground line-through'
                  : 'text-foreground'
              }`}
            >
              {item.title}
            </Text>
            <Pressable
              onPress={() => deleteTodo.mutate(item.id)}
              hitSlop={8}
              className="w-8 h-8 items-center justify-center rounded-full active:bg-muted"
            >
              <Trash2Icon size={16} className="text-muted-foreground" />
            </Pressable>
          </Pressable>
        )}
        ItemSeparatorComponent={() => <View className="h-px bg-border" />}
      />

      <View className="absolute bottom-0 left-0 right-0 px-5 pb-8 pt-4 bg-background">
        <View className="flex-row items-center gap-3 bg-card rounded-2xl px-4 py-1 border border-border">
          <TextInput
            value={newTitle}
            onChangeText={setNewTitle}
            onSubmitEditing={handleAdd}
            placeholder="Add a new task..."
            placeholderTextColor={isDark ? '#78716c' : '#a8a29e'}
            className="flex-1 text-foreground text-base py-3"
            returnKeyType="done"
          />
          <Pressable
            onPress={handleAdd}
            disabled={!newTitle.trim() || addTodo.isPending}
            className="w-9 h-9 rounded-full bg-primary items-center justify-center active:scale-[0.95] disabled:opacity-40"
          >
            <PlusIcon size={18} className="text-primary-foreground" />
          </Pressable>
        </View>
      </View>
    </SafeAreaView>
  );
}
