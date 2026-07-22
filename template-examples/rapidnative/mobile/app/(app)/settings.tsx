import { View, Text, ScrollView, Pressable } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import Constants from 'expo-constants';
import { ChevronRightIcon, SettingsIcon, BellIcon, PaintbrushIcon, InfoIcon, ArrowLeftIcon, UserIcon, HelpCircleIcon } from 'lucide-react-native';
import { cssInterop, useColorScheme } from 'nativewind';
import { router } from 'expo-router';

cssInterop(ChevronRightIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(SettingsIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(BellIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(PaintbrushIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(InfoIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(ArrowLeftIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(UserIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(HelpCircleIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });

const SETTINGS_SECTIONS = [
  {
    title: 'Preferences',
    items: [
      { icon: UserIcon, label: 'Profile', value: '', color: 'text-chart-1', route: '/profile' as const },
      { icon: PaintbrushIcon, label: 'Appearance', value: 'System', color: 'text-chart-2' },
      { icon: BellIcon, label: 'Notifications', value: 'On', color: 'text-chart-3' },
    ],
  },
  {
    title: 'About',
    items: [
      { icon: InfoIcon, label: 'Version', value: '1.0.0', color: 'text-muted-foreground' },
      { icon: HelpCircleIcon, label: 'FAQ', value: '', color: 'text-chart-2', route: '/faq' as const },
    ],
  },
];

export default function SettingsScreen() {
  const { colorScheme } = useColorScheme();
  const isDark = colorScheme === 'dark';
  const appName = Constants.expoConfig?.name ?? 'Todos';

  return (
    <SafeAreaView edges={['top']} className="flex-1 bg-background">
      <ScrollView
        contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 40 }}
        keyboardShouldPersistTaps="handled"
      >
        {/* Header */}
        <View className="flex-row items-center gap-3 pt-4 pb-6">
          <Pressable
            onPress={() => router.back()}
            className="w-10 h-10 rounded-full items-center justify-center bg-muted active:scale-[0.95]"
          >
            <ArrowLeftIcon size={20} className="text-foreground" />
          </Pressable>
          <View>
            <Text className="text-foreground text-2xl font-bold tracking-tight">
              Settings
            </Text>
            <Text className="text-muted-foreground text-sm">{appName}</Text>
          </View>
        </View>

        {/* App info card */}
        <View className="bg-card rounded-2xl p-5 mb-6 items-center gap-2">
          <View className="w-16 h-16 rounded-2xl bg-primary items-center justify-center mb-1">
            <SettingsIcon size={28} className="text-primary-foreground" />
          </View>
          <Text className="text-foreground text-lg font-semibold">{appName}</Text>
          <Text className="text-muted-foreground text-sm">Stay organized</Text>
        </View>

        {/* Settings sections */}
        {SETTINGS_SECTIONS.map((section) => (
          <View key={section.title} className="mb-6">
            <Text className="text-muted-foreground text-xs uppercase tracking-wider mb-3 px-1">
              Settings
            </Text>
            <View className="bg-card rounded-2xl overflow-hidden">
              {section.items.map((item, index) => {
                const Icon = item.icon;
                return (
                  <Pressable
                    key={item.label}
                    onPress={() => {
                      if ('route' in item && item.route) router.push(item.route);
                    }}
                    className={`flex-row items-center gap-4 px-4 py-3.5 active:bg-muted/50 ${
                      index < section.items.length - 1 ? 'border-b border-border' : ''
                    }`}
                  >
                    <View className="w-9 h-9 rounded-xl bg-muted items-center justify-center">
                      <Icon size={18} className={item.color} />
                    </View>
                    <Text className="text-foreground text-base flex-1">{item.label}</Text>
                    <Text className="text-muted-foreground text-sm mr-1">{item.value}</Text>
                    <ChevronRightIcon size={16} className="text-muted-foreground" />
                  </Pressable>
                );
              })}
            </View>
          </View>
        ))}
      </ScrollView>
    </SafeAreaView>
  );
}
