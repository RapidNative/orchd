import { useState } from 'react';
import { View, Text, ScrollView, Pressable } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import Constants from 'expo-constants';
import { ArrowLeftIcon, ChevronDownIcon, ChevronUpIcon, HelpCircleIcon } from 'lucide-react-native';
import { cssInterop } from 'nativewind';
import { router } from 'expo-router';

cssInterop(ArrowLeftIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(ChevronDownIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(ChevronUpIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });
cssInterop(HelpCircleIcon, { className: { target: 'style', nativeStyleToProp: { color: true } } });

const FAQ_ITEMS = [
  {
    question: 'How do I create a new task?',
    answer: 'Type your task into the input field at the bottom of the home screen and tap the plus button. Your new todo will appear instantly in your list.',
  },
  {
    question: 'How do I mark a task as complete?',
    answer: "Simply tap the circle next to any task to toggle it between complete and incomplete. Completed tasks show a checkmark and a strikethrough for easy identification.",
  },
  {
    question: 'Can I delete a task?',
    answer: 'Yes! Tap the trash icon on the right side of any task to permanently remove it from your list.',
  },
  {
    question: 'How do I see my progress?',
    answer: 'Visit the Profile page from Settings to see your stats — including total tasks, completed count, and your completion rate.',
  },
  {
    question: 'Is my data backed up?',
    answer: 'Your todos are stored securely and synced automatically. You can access them anytime you open the app.',
  },
  {
    question: 'Can I reorder my tasks?',
    answer: 'Tasks are displayed in the order they were created. Reordering is coming in a future update!',
  },
];

function AccordionItem({ question, answer }: { question: string; answer: string }) {
  const [open, setOpen] = useState(false);

  return (
    <View className="border-b border-border">
      <Pressable
        onPress={() => setOpen(!open)}
        className="flex-row items-center justify-between py-4 px-1 active:opacity-60"
      >
        <Text className="text-foreground text-base font-medium flex-1 mr-4">{question}</Text>
        {open ? (
          <ChevronUpIcon size={18} className="text-muted-foreground" />
        ) : (
          <ChevronDownIcon size={18} className="text-muted-foreground" />
        )}
      </Pressable>
      {open && (
        <View className="pb-4 px-1">
          <Text className="text-muted-foreground text-sm leading-relaxed">{answer}</Text>
        </View>
      )}
    </View>
  );
}

export default function FaqScreen() {
  const appName = Constants.expoConfig?.name ?? 'Todos';

  return (
    <SafeAreaView edges={['top']} className="flex-1 bg-background">
      <ScrollView
        contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 60 }}
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
            <Text className="text-foreground text-2xl font-bold tracking-tight">FAQ</Text>
            <Text className="text-muted-foreground text-sm">Frequently asked questions</Text>
          </View>
        </View>

        {/* Hero card */}
        <View className="bg-card rounded-2xl p-5 mb-6 items-center gap-2">
          <View className="w-16 h-16 rounded-2xl bg-primary/10 items-center justify-center mb-1">
            <HelpCircleIcon size={28} className="text-primary" />
          </View>
          <Text className="text-foreground text-lg font-semibold">Need help with {appName}?</Text>
          <Text className="text-muted-foreground text-sm text-center">
            Find answers to common questions below
          </Text>
        </View>

        {/* FAQ Accordion */}
        <View className="bg-card rounded-2xl overflow-hidden px-4">
          {FAQ_ITEMS.map((item) => (
            <AccordionItem key={item.question} question={item.question} answer={item.answer} />
          ))}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}
