import { View, Text, ScrollView, Pressable } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import Constants from 'expo-constants';
import { ArrowLeftIcon, UserIcon, CheckIcon, BellIcon, InfoIcon } from 'lucide-react-native';
import { cssInterop } from 'nativewind';
import { router } from 'expo-router';
import { useQuery } from '@tanstack/react-query';
import { useApp } from '@/src/hooks';

cssInterop(ArrowLeftIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(UserIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(CheckIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(BellIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(InfoIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });

export default function ProfileScreen() {
  const { client } = useApp();
  const appName = Constants.expoConfig?.name ?? 'Todos';

  const { data: todos } = useQuery({
    queryKey: ['todos'],
    queryFn: async () => {
      const { data, error } = await client.from('todos').select('*');
      if (error) throw error;
      return data ?? [];
    },
  });

  const completedCount = todos?.filter((todo) => todo.completed).length ?? 0;
  const totalCount = todos?.length ?? 0;
  const completionRate = totalCount > 0 ? Math.round((completedCount / totalCount) * 100) : 0;

  const stats = [
    { icon: CheckIcon, label: 'Completed', value: `${completedCount}`, colorClass: 'text-chart-1' },
    { icon: BellIcon, label: 'Total tasks', value: `${totalCount}`, colorClass: 'text-chart-2' },
    { icon: InfoIcon, label: 'Completion rate', value: `${completionRate}%`, colorClass: 'text-chart-3' },
  ];

  return (
    <SafeAreaView edges={['top']} className="flex-1 bg-background">
      <ScrollView contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 40 }}>
        <View className="flex-row items-center gap-3 pt-4 pb-6">
          <Pressable
            onPress={() => router.back()}
            className="w-10 h-10 rounded-full items-center justify-center bg-muted active:scale-[0.95]"
          >
            <ArrowLeftIcon size={20} className="text-foreground" />
          </Pressable>
          <View>
            <Text className="text-foreground text-2xl font-bold tracking-tight">Profile</Text>
            <Text className="text-muted-foreground text-sm">{appName}</Text>
          </View>
        </View>

        <View className="bg-card rounded-2xl p-6 mb-6 items-center gap-3">
          <View className="w-20 h-20 rounded-full bg-primary/10 items-center justify-center">
            <UserIcon size={34} className="text-primary" />
          </View>
          <View className="items-center gap-1">
            <Text className="text-foreground text-xl font-semibold">You</Text>
            <Text className="text-muted-foreground text-sm">Todo enthusiast</Text>
          </View>
        </View>

        <Text className="text-muted-foreground text-xs uppercase tracking-wider mb-3 px-1">
          Your stats
        </Text>
        <View className="flex-row gap-3 mb-6">
          {stats.map((stat) => {
            const Icon = stat.icon;
            return (
              <View
                key={stat.label}
                className="flex-1 bg-card rounded-2xl p-4 items-center gap-2"
              >
                <View className="w-10 h-10 rounded-xl bg-muted items-center justify-center">
                  <Icon size={20} className={stat.colorClass} />
                </View>
                <Text className="text-foreground text-2xl font-bold">{stat.value}</Text>
                <Text className="text-muted-foreground text-xs text-center">{stat.label}</Text>
              </View>
            );
          })}
        </View>

        <Text className="text-muted-foreground text-xs uppercase tracking-wider mb-3 px-1">
          Recent activity
        </Text>
        <View className="bg-card rounded-2xl overflow-hidden">
          <View className="px-4 py-3.5 border-b border-border">
            <Text className="text-foreground text-base">Joined {appName}</Text>
            <Text className="text-muted-foreground text-sm">Just now</Text>
          </View>
          <View className="px-4 py-3.5 border-b border-border">
            <Text className="text-foreground text-base">Completed first task</Text>
            <Text className="text-muted-foreground text-sm">Today</Text>
          </View>
          <View className="px-4 py-3.5">
            <Text className="text-foreground text-base">Created new todos</Text>
            <Text className="text-muted-foreground text-sm">Today</Text>
          </View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}
