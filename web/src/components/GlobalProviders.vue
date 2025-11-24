<script setup lang="ts">
import { appState } from "@/utils/app-state";
import { actualTheme } from "@/utils/theme";
import { getLocale } from "@/locales";
import {
  darkTheme,
  NConfigProvider,
  NDialogProvider,
  NLoadingBarProvider,
  NMessageProvider,
  useLoadingBar,
  useMessage,
  type GlobalTheme,
  type GlobalThemeOverrides,
  zhCN,
  enUS,
  jaJP,
  dateZhCN,
  dateEnUS,
  dateJaJP,
} from "naive-ui";
import { computed, defineComponent, watch } from "vue";

// Custom theme overrides - dynamically adjusted based on current theme
const themeOverrides = computed<GlobalThemeOverrides>(() => {
  const baseOverrides: GlobalThemeOverrides = {
    common: {
      primaryColor: "#667eea",
      primaryColorHover: "#5a6fd8",
      primaryColorPressed: "#4c63d2",
      primaryColorSuppl: "#8b9df5",
      borderRadius: "12px",
      borderRadiusSmall: "8px",
      fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
    },
    Card: {
      paddingMedium: "24px",
    },
    Button: {
      fontWeight: "600",
      heightMedium: "40px",
      heightLarge: "48px",
    },
    Input: {
      heightMedium: "40px",
      heightLarge: "48px",
    },
    Menu: {
      itemHeight: "42px",
    },
    LoadingBar: {
      colorLoading: "#667eea",
      colorError: "#ff4757",
      height: "3px",
    },
  };

  // Extra overrides for dark mode
  if (actualTheme.value === "dark") {
    return {
      ...baseOverrides,
      common: {
        ...baseOverrides.common,
        // Layered contrast: lighter outer background, deep dark content
        bodyColor: "#2b3038", // Outer background - light gray
        cardColor: "#0f1115", // Card content - deep dark
        modalColor: "#0f1115", // Modal - deep dark
        popoverColor: "#0f1115", // 弹出层 - 深黑色
        tableColor: "#0f1115", // 表格 - 深黑色
        inputColor: "#1a1d23", // 输入框 - 稍深一点
        actionColor: "#1a1d23", // 操作区域
        textColorBase: "#e8e8e8", // 文字 - 浅色高对比
        textColor1: "#e8e8e8",
        textColor2: "#b4b4b4",
        textColor3: "#888888",
        borderColor: "rgba(255, 255, 255, 0.08)",
        dividerColor: "rgba(255, 255, 255, 0.05)",
      },
      Card: {
        ...baseOverrides.Card,
        color: "#0f1115", // 卡片背景 - 深黑色
        textColor: "#e8e8e8",
        borderColor: "rgba(255, 255, 255, 0.08)",
      },
      Input: {
        ...baseOverrides.Input,
        color: "#1a1d23", // 输入框背景
        textColor: "#e8e8e8",
        colorFocus: "#1a1d23",
        borderHover: "rgba(102, 126, 234, 0.5)",
        borderFocus: "rgba(102, 126, 234, 0.8)",
        placeholderColor: "#666666",
      },
      Select: {
        peers: {
          InternalSelection: {
            textColor: "#e8e8e8",
            color: "#1a1d23",
            placeholderColor: "#666666",
          },
        },
      },
      DataTable: {
        tdColor: "#0f1115", // 表格单元格 - 深黑色
        thColor: "#1a1d23", // 表头 - 稍深
        thTextColor: "#e8e8e8",
        tdTextColor: "#e8e8e8",
        borderColor: "rgba(255, 255, 255, 0.08)",
      },
      Tag: {
        textColor: "#e8e8e8",
      },
      Pagination: {
        itemTextColor: "#b4b4b4",
        itemTextColorActive: "#e8e8e8",
        itemColor: "#1a1d23",
        itemColorActive: "#282c37",
      },
      DatePicker: {
        itemTextColor: "#e8e8e8",
        itemColorActive: "#1a1d23",
        panelColor: "#0f1115",
      },
      Message: {
        color: "#323841", // 消息背景 - 浅灰色，比内容区域浅
        textColor: "#e8e8e8",
        iconColor: "#e8e8e8",
        borderRadius: "8px",
        colorInfo: "#323841",
        colorSuccess: "#323841",
        colorWarning: "#323841",
        colorError: "#323841",
        colorLoading: "#323841",
      },
      LoadingBar: {
        ...baseOverrides.LoadingBar,
      },
      Notification: {
        color: "#323841", // 通知背景 - 浅灰色
        textColor: "#e8e8e8",
        titleTextColor: "#e8e8e8",
        descriptionTextColor: "#b4b4b4",
        borderRadius: "8px",
      },
    };
  }

  return baseOverrides;
});

// Return theme object based on current theme
const theme = computed<GlobalTheme | undefined>(() => {
  return actualTheme.value === "dark" ? darkTheme : undefined;
});

// Return Naive UI locale based on current language
const locale = computed(() => {
  const currentLocale = getLocale();
  switch (currentLocale) {
    case "zh-CN":
      return zhCN;
    case "en-US":
      return enUS;
    case "ja-JP":
      return jaJP;
    default:
      return zhCN;
  }
});

// Return date-fns locale based on current language
const dateLocale = computed(() => {
  const currentLocale = getLocale();
  switch (currentLocale) {
    case "zh-CN":
      return dateZhCN;
    case "en-US":
      return dateEnUS;
    case "ja-JP":
      return dateJaJP;
    default:
      return dateZhCN;
  }
});

function useGlobalMessage() {
  window.$message = useMessage();
}

const LoadingBar = defineComponent({
  setup() {
    const loadingBar = useLoadingBar();
    watch(
      () => appState.loading,
      loading => {
        if (loading) {
          loadingBar.start();
        } else {
          loadingBar.finish();
        }
      }
    );
    return () => null;
  },
});

const Message = defineComponent({
  setup() {
    useGlobalMessage();
    return () => null;
  },
});
</script>

<template>
  <n-config-provider
    :theme="theme"
    :theme-overrides="themeOverrides"
    :locale="locale"
    :date-locale="dateLocale"
  >
    <n-loading-bar-provider>
      <n-message-provider placement="top-right">
        <n-dialog-provider>
          <slot />
          <loading-bar />
          <message />
        </n-dialog-provider>
      </n-message-provider>
    </n-loading-bar-provider>
  </n-config-provider>
</template>
