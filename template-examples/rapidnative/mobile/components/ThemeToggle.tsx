import { useColorScheme } from 'nativewind';
import { TouchableOpacity } from 'react-native';
import { SunIcon, MoonIcon } from 'lucide-react-native';

export function ThemeToggle() {
  const { colorScheme, setColorScheme } = useColorScheme();

  return (
    <TouchableOpacity onPress={() => setColorScheme(colorScheme === 'dark' ? 'light' : 'dark')}>
      {colorScheme === 'dark' ? (
        <SunIcon className="text-foreground" size={24} />
      ) : (
        <MoonIcon className="text-foreground" size={24} />
      )}
    </TouchableOpacity>
  );
}
