import { computed, ref, watch } from "vue";

// Theme mode types
export type ThemeMode = "auto" | "light" | "dark";
export type ActualTheme = "light" | "dark";

// Storage key name
const THEME_KEY = "gpt-load-theme-mode";

// Get initial theme mode
function getInitialThemeMode(): ThemeMode {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored && ["auto", "light", "dark"].includes(stored)) {
    return stored as ThemeMode;
  }
  return "auto"; // Use auto mode by default
}

// Detect system theme preference
function getSystemTheme(): ActualTheme {
  if (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches) {
    return "dark";
  }
  return "light";
}

// Theme mode selected by the user
export const themeMode = ref<ThemeMode>(getInitialThemeMode());

// System theme (auto-detected)
const systemTheme = ref<ActualTheme>(getSystemTheme());

// Actual theme to be used
export const actualTheme = computed<ActualTheme>(() => {
  if (themeMode.value === "auto") {
    return systemTheme.value;
  }
  return themeMode.value as ActualTheme;
});

// Whether current theme is dark mode
export const isDark = computed(() => actualTheme.value === "dark");

// Switch theme mode
export function setThemeMode(mode: ThemeMode) {
  themeMode.value = mode;
  localStorage.setItem(THEME_KEY, mode);
}

// Cycle through theme modes (used for toggle button)
export function toggleTheme() {
  const modes: ThemeMode[] = ["auto", "light", "dark"];
  const currentIndex = modes.indexOf(themeMode.value);
  const nextIndex = (currentIndex + 1) % modes.length;
  const nextMode = modes[nextIndex] ?? "auto";
  setThemeMode(nextMode);
}

// Listen to system theme changes
if (window.matchMedia) {
  const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");

  // Update system theme
  const updateSystemTheme = (e: MediaQueryListEvent | MediaQueryList) => {
    systemTheme.value = e.matches ? "dark" : "light";
  };

  // Add event listener
  if (mediaQuery.addEventListener) {
    mediaQuery.addEventListener("change", updateSystemTheme);
  } else if (mediaQuery.addListener) {
    // Backward compatibility for older browsers
    mediaQuery.addListener(updateSystemTheme as (event: MediaQueryListEvent) => void);
  }
}

// Update HTML root element class (used for CSS variable switching)
watch(
  actualTheme,
  theme => {
    const html = document.documentElement;
    if (theme === "dark") {
      html.classList.add("dark");
      html.classList.remove("light");
    } else {
      html.classList.add("light");
      html.classList.remove("dark");
    }
  },
  { immediate: true }
);
