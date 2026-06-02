<script setup lang="ts">
import { getDashboardTokenUsage, type DashboardChartRange } from "@/api/dashboard";
import type { ChartData, DashboardTokenUsageResponse, ModelTokenUsageItem } from "@/types/models";
import { NEmpty, NSelect, NSpin, type SelectOption } from "naive-ui";
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const props = withDefaults(
  defineProps<{
    range?: DashboardChartRange;
  }>(),
  {
    range: "today",
  }
);

const emit = defineEmits<{
  "update:range": [value: DashboardChartRange];
}>();

const loading = ref(true);
const tokenUsage = ref<DashboardTokenUsageResponse | null>(null);
const selectedModel = ref<string | null>(null);
const selectedRange = ref<DashboardChartRange>("today");
const modelOptions = ref<SelectOption[]>([]);
const chartSvg = ref<SVGElement>();
const hoveredPoint = ref<{
  pointIndex: number;
  x: number;
  y: number;
} | null>(null);
const tooltipData = ref<{
  time: string;
  datasets: Array<{
    label: string;
    value: number;
    color: string;
  }>;
} | null>(null);
const tooltipPosition = ref({ x: 0, y: 0 });
let tokenPanelMounted = false;
let tokenUsageRequestSeq = 0;

const chartWidth = 800;
const chartHeight = 260;
const chartPadding = { top: 40, right: 40, bottom: 60, left: 80 };
const plotWidth = chartWidth - chartPadding.left - chartPadding.right;
const plotHeight = chartHeight - chartPadding.top - chartPadding.bottom;

const timeRanges: Array<{ value: DashboardChartRange; labelKey: string }> = [
  { value: "today", labelKey: "charts.rangeToday" },
  { value: "yesterday", labelKey: "charts.rangeYesterday" },
  { value: "this_week", labelKey: "charts.rangeThisWeek" },
  { value: "last_week", labelKey: "charts.rangeLastWeek" },
  { value: "this_month", labelKey: "charts.rangeThisMonth" },
  { value: "last_month", labelKey: "charts.rangeLastMonth" },
  { value: "last_30_days", labelKey: "dashboard.last30Days" },
];

const formatTokenCount = (value: number) => {
  const roundedValue = Math.round(value);
  const absValue = Math.abs(roundedValue);
  const units = [
    { threshold: 1_000_000_000_000, suffix: "T" },
    { threshold: 1_000_000_000, suffix: "B" },
    { threshold: 1_000_000, suffix: "M" },
    { threshold: 1_000, suffix: "K" },
  ];

  for (const unit of units) {
    if (absValue >= unit.threshold) {
      const scaled = roundedValue / unit.threshold;
      const fractionDigits = Math.abs(scaled) >= 100 ? 0 : 1;
      return `${scaled.toFixed(fractionDigits)}${unit.suffix}`;
    }
  }
  return roundedValue.toString();
};

const formatExactTokenCount = (value: number) => {
  return new Intl.NumberFormat().format(Math.round(value));
};

const topModels = computed<ModelTokenUsageItem[]>(() => tokenUsage.value?.models ?? []);
const summary = computed(() => tokenUsage.value?.summary);
const tokenChart = computed<ChartData | null>(() => tokenUsage.value?.chart ?? null);
const hasEstimatedTokens = computed(() => (summary.value?.estimated_tokens ?? 0) > 0);
const selectedRangeLabel = computed(() => {
  const range = timeRanges.find(item => item.value === selectedRange.value);
  return range ? t(range.labelKey) : "";
});
const rangeOptions = computed<SelectOption[]>(() =>
  timeRanges.map(range => ({
    value: range.value,
    label: t(range.labelKey),
  }))
);

const chartDatasetTotals = computed(() => {
  if (!tokenChart.value) {
    return [] as number[];
  }

  return tokenChart.value.datasets.map(dataset =>
    dataset.data.reduce((sum, value) => sum + (Number.isFinite(value) ? value : 0), 0)
  );
});

const chartHasData = computed(() => chartDatasetTotals.value.some(total => total > 0));

const dataRange = computed(() => {
  if (!tokenChart.value) {
    return { min: 0, max: 10 };
  }

  const values = tokenChart.value.datasets
    .flatMap(dataset => dataset.data)
    .filter(value => Number.isFinite(value));
  const max = Math.max(...values, 0);

  if (max === 0) {
    return { min: 0, max: 10 };
  }

  return {
    min: 0,
    max: max + Math.max(max * 0.1, 1),
  };
});

const yTicks = computed(() => {
  const { min, max } = dataRange.value;
  const range = max - min;
  const tickCount = 5;
  const step = range / (tickCount - 1);

  return Array.from({ length: tickCount }, (_, i) => min + i * step);
});

const showDateInLabels = computed(() => (tokenChart.value?.labels.length ?? 0) > 24);
const showDataPoints = computed(() => (tokenChart.value?.labels.length ?? 0) <= 200);

const formatTimeLabel = (isoString: string) => {
  const date = new Date(isoString);
  if (Number.isNaN(date.getTime())) {
    return isoString;
  }

  const timePart = date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });

  if (!showDateInLabels.value) {
    return timePart;
  }

  const datePart = date.toLocaleDateString(undefined, {
    month: "2-digit",
    day: "2-digit",
  });

  return `${datePart} ${timePart}`;
};

const visibleLabels = computed(() => {
  if (!tokenChart.value) {
    return [];
  }

  const labels = tokenChart.value.labels;
  const maxLabels = 8;
  const step = Math.max(1, Math.ceil(labels.length / maxLabels));

  return labels
    .map((label, index) => ({ text: formatTimeLabel(label), index }))
    .filter((_, i) => i % step === 0);
});

const getXPosition = (index: number) => {
  if (!tokenChart.value) {
    return 0;
  }
  const totalPoints = tokenChart.value.labels.length;
  if (totalPoints <= 1) {
    return chartPadding.left + plotWidth / 2;
  }
  return chartPadding.left + (index / (totalPoints - 1)) * plotWidth;
};

const getYPosition = (value: number) => {
  const { min, max } = dataRange.value;
  const ratio = (value - min) / (max - min);
  return chartPadding.top + (1 - ratio) * plotHeight;
};

const generateLinePath = (data: number[]) => {
  if (data.length === 0) {
    return "";
  }

  return data
    .map((value, index) => {
      const command = index === 0 ? "M" : "L";
      return `${command} ${getXPosition(index)},${getYPosition(value)}`;
    })
    .join(" ");
};

const generateAreaPath = (data: number[]) => {
  if (data.length === 0) {
    return "";
  }

  const baseY = getYPosition(dataRange.value.min);
  const points = data
    .map((value, index) => `L ${getXPosition(index)},${getYPosition(value)}`)
    .join(" ");
  const firstX = getXPosition(0);
  const lastX = getXPosition(data.length - 1);

  return `M ${firstX},${baseY} ${points} L ${lastX},${baseY} Z`;
};

const handleChartMouseMove = (event: MouseEvent) => {
  if (!tokenChart.value || !chartSvg.value) {
    return;
  }

  const rect = chartSvg.value.getBoundingClientRect();
  const scaleX = chartWidth / rect.width;
  const scaleY = chartHeight / rect.height;
  const mouseX = (event.clientX - rect.left) * scaleX;
  const mouseY = (event.clientY - rect.top) * scaleY;
  const totalPoints = tokenChart.value.labels.length;

  if (totalPoints <= 0) {
    hoveredPoint.value = null;
    tooltipData.value = null;
    return;
  }

  let closestTimeIndex = 0;
  if (totalPoints > 1) {
    const ratio = (mouseX - chartPadding.left) / plotWidth;
    closestTimeIndex = Math.round(ratio * (totalPoints - 1));
    closestTimeIndex = Math.max(0, Math.min(totalPoints - 1, closestTimeIndex));
  }

  const closestXDistance = Math.abs(mouseX - getXPosition(closestTimeIndex));
  if (closestXDistance > 50) {
    hoveredPoint.value = null;
    tooltipData.value = null;
    return;
  }

  const datasetsAtTime = tokenChart.value.datasets.map(dataset => ({
    label: dataset.label,
    value: dataset.data[closestTimeIndex] ?? 0,
    color: dataset.color,
  }));

  hoveredPoint.value = {
    pointIndex: closestTimeIndex,
    x: mouseX,
    y: mouseY,
  };

  const x = getXPosition(closestTimeIndex);
  const totalY = datasetsAtTime.reduce((sum, item) => sum + getYPosition(item.value), 0);
  const avgY =
    datasetsAtTime.length > 0 ? totalY / datasetsAtTime.length : getYPosition(dataRange.value.min);

  tooltipPosition.value = {
    x: (x / chartWidth) * rect.width,
    y: ((avgY - 20) / chartHeight) * rect.height,
  };

  const label = tokenChart.value.labels[closestTimeIndex];
  if (!label) {
    hoveredPoint.value = null;
    tooltipData.value = null;
    return;
  }

  tooltipData.value = {
    time: formatTimeLabel(label),
    datasets: datasetsAtTime,
  };
};

const hideChartTooltip = () => {
  hoveredPoint.value = null;
  tooltipData.value = null;
};

const loadTokenUsage = async () => {
  const requestSeq = ++tokenUsageRequestSeq;
  try {
    loading.value = true;
    const response = await getDashboardTokenUsage(
      undefined,
      selectedRange.value,
      10,
      selectedModel.value?.trim()
    );
    if (requestSeq !== tokenUsageRequestSeq) {
      return;
    }
    tokenUsage.value = response.data;
    const knownModels = new Map(modelOptions.value.map(option => [String(option.value), option]));
    for (const item of response.data.models) {
      knownModels.set(item.model, { label: item.model, value: item.model });
    }
    modelOptions.value = Array.from(knownModels.values()).sort((a, b) =>
      String(a.label).localeCompare(String(b.label))
    );
  } catch (error) {
    if (requestSeq !== tokenUsageRequestSeq) {
      return;
    }
    console.error("Failed to load token usage:", error);
    tokenUsage.value = null;
  } finally {
    if (requestSeq === tokenUsageRequestSeq) {
      loading.value = false;
    }
  }
};

const handleModelChange = (value: string | number | null) => {
  selectedModel.value = value === null ? null : String(value).trim() || null;
  loadTokenUsage();
};

const handleRangeChange = (value: string | number | null) => {
  if (value === null) {
    selectedRange.value = "today";
    return;
  }
  selectedRange.value = value as DashboardChartRange;
};

watch(
  () => props.range,
  value => {
    if (selectedRange.value !== value) {
      selectedRange.value = value;
    }
  },
  { immediate: true }
);

watch(selectedRange, value => {
  if (!tokenPanelMounted) {
    return;
  }
  if (value !== props.range) {
    emit("update:range", value);
  }
  loadTokenUsage();
});

onMounted(() => {
  tokenPanelMounted = true;
  loadTokenUsage();
});
</script>

<template>
  <div class="token-panel">
    <div class="token-panel-header">
      <div class="token-title-section">
        <h3 class="token-panel-title">{{ t("dashboard.modelTokenUsage") }}</h3>
        <p class="token-panel-subtitle">{{ selectedRangeLabel }}</p>
      </div>
      <div class="token-panel-controls">
        <n-select
          class="token-range-filter"
          :value="selectedRange"
          :options="rangeOptions"
          :placeholder="t('charts.timeRange')"
          size="small"
          @update:value="handleRangeChange"
        />
        <n-select
          class="token-model-filter"
          :value="selectedModel"
          :options="modelOptions"
          :placeholder="t('dashboard.allModels')"
          clearable
          filterable
          tag
          size="small"
          :aria-label="t('dashboard.modelFilter')"
          @update:value="handleModelChange"
        />
      </div>
      <div class="token-panel-total" :title="formatExactTokenCount(summary?.total_tokens ?? 0)">
        <span class="token-total-label">{{ t("dashboard.totalTokens") }}</span>
        <strong>{{ formatTokenCount(summary?.total_tokens ?? 0) }}</strong>
        <span v-if="hasEstimatedTokens" class="token-panel-estimated">
          {{ t("dashboard.includesEstimated") }}
          {{ formatTokenCount(summary?.estimated_tokens ?? 0) }}
        </span>
      </div>
    </div>

    <n-spin :show="loading">
      <div v-if="tokenChart && chartHasData" class="token-chart-block">
        <div class="token-chart-wrapper">
          <div class="token-chart-legend">
            <div
              v-for="(dataset, datasetIndex) in tokenChart.datasets"
              :key="dataset.label"
              class="token-legend-item"
              :title="formatExactTokenCount(chartDatasetTotals[datasetIndex] ?? 0)"
            >
              <div class="token-legend-indicator" :style="{ backgroundColor: dataset.color }" />
              <span class="token-legend-label">{{ dataset.label }}</span>
              <span class="token-legend-total">
                {{ formatTokenCount(chartDatasetTotals[datasetIndex] ?? 0) }}
              </span>
            </div>
          </div>

          <svg
            ref="chartSvg"
            viewBox="0 0 800 260"
            class="token-chart-svg"
            @mousemove="handleChartMouseMove"
            @mouseleave="hideChartTooltip"
          >
            <defs>
              <pattern id="token-grid" width="40" height="30" patternUnits="userSpaceOnUse">
                <path
                  d="M 40 0 L 0 0 0 30"
                  fill="none"
                  :stroke="`var(--chart-grid)`"
                  stroke-width="1"
                  opacity="0.3"
                />
              </pattern>
            </defs>
            <rect width="100%" height="100%" fill="url(#token-grid)" />

            <g class="token-y-axis">
              <line
                :x1="chartPadding.left"
                :y1="chartPadding.top"
                :x2="chartPadding.left"
                :y2="chartHeight - chartPadding.bottom"
                :stroke="`var(--chart-axis)`"
                stroke-width="2"
              />
              <g v-for="(tick, index) in yTicks" :key="index">
                <line
                  :x1="chartPadding.left - 5"
                  :y1="getYPosition(tick)"
                  :x2="chartPadding.left"
                  :y2="getYPosition(tick)"
                  :stroke="`var(--chart-text)`"
                  stroke-width="1"
                />
                <text
                  :x="chartPadding.left - 10"
                  :y="getYPosition(tick) + 4"
                  text-anchor="end"
                  class="token-axis-label"
                >
                  {{ formatTokenCount(tick) }}
                </text>
              </g>
            </g>

            <g class="token-x-axis">
              <line
                :x1="chartPadding.left"
                :y1="chartHeight - chartPadding.bottom"
                :x2="chartWidth - chartPadding.right"
                :y2="chartHeight - chartPadding.bottom"
                :stroke="`var(--chart-axis)`"
                stroke-width="2"
              />
              <g v-for="(label, index) in visibleLabels" :key="index">
                <line
                  :x1="getXPosition(label.index)"
                  :y1="chartHeight - chartPadding.bottom"
                  :x2="getXPosition(label.index)"
                  :y2="chartHeight - chartPadding.bottom + 5"
                  :stroke="`var(--chart-text)`"
                  stroke-width="1"
                />
                <text
                  :x="getXPosition(label.index)"
                  :y="chartHeight - chartPadding.bottom + 18"
                  text-anchor="middle"
                  class="token-axis-label"
                >
                  {{ label.text }}
                </text>
              </g>
            </g>

            <g v-for="(dataset, datasetIndex) in tokenChart.datasets" :key="dataset.label">
              <defs>
                <linearGradient
                  :id="`token-gradient-${datasetIndex}`"
                  x1="0%"
                  y1="0%"
                  x2="0%"
                  y2="100%"
                >
                  <stop offset="0%" :stop-color="dataset.color" stop-opacity="0.2" />
                  <stop offset="100%" :stop-color="dataset.color" stop-opacity="0.04" />
                </linearGradient>
              </defs>

              <path
                v-if="datasetIndex === 0"
                :d="generateAreaPath(dataset.data)"
                :fill="`url(#token-gradient-${datasetIndex})`"
                class="token-area-path"
              />
              <path
                :d="generateLinePath(dataset.data)"
                :stroke="dataset.color"
                :stroke-width="datasetIndex === 0 ? 2.5 : 2"
                fill="none"
                class="token-line-path"
                :style="{ opacity: datasetIndex === 0 ? 1 : 0.86 }"
              />

              <g v-for="(value, pointIndex) in dataset.data" :key="pointIndex">
                <circle
                  v-if="showDataPoints && value > 0"
                  :cx="getXPosition(pointIndex)"
                  :cy="getYPosition(value)"
                  :r="datasetIndex === 0 ? 3 : 2.5"
                  :fill="dataset.color"
                  :stroke="dataset.color"
                  stroke-width="1"
                  class="token-data-point"
                  :class="{ 'token-point-hover': hoveredPoint?.pointIndex === pointIndex }"
                  :style="{ opacity: datasetIndex === 0 ? 1 : 0.86 }"
                />
              </g>
            </g>

            <line
              v-if="hoveredPoint"
              :x1="getXPosition(hoveredPoint.pointIndex)"
              :y1="chartPadding.top"
              :x2="getXPosition(hoveredPoint.pointIndex)"
              :y2="chartHeight - chartPadding.bottom"
              stroke="#999"
              stroke-width="1"
              stroke-dasharray="5,5"
              opacity="0.7"
            />
          </svg>

          <div
            v-if="tooltipData"
            class="token-chart-tooltip"
            :style="{
              left: tooltipPosition.x + 'px',
              top: tooltipPosition.y + 'px',
            }"
          >
            <div class="token-tooltip-time">{{ tooltipData.time }}</div>
            <div
              v-for="dataset in tooltipData.datasets"
              :key="dataset.label"
              class="token-tooltip-value"
            >
              <span class="token-tooltip-color" :style="{ backgroundColor: dataset.color }" />
              {{ dataset.label }}: {{ formatTokenCount(dataset.value) }}
            </div>
          </div>
        </div>
      </div>

      <div v-if="summary" class="token-summary-grid">
        <div class="summary-item input" :title="formatExactTokenCount(summary.input_tokens)">
          <span>{{ t("dashboard.inputTokens") }}</span>
          <strong>{{ formatTokenCount(summary.input_tokens) }}</strong>
        </div>
        <div class="summary-item output" :title="formatExactTokenCount(summary.output_tokens)">
          <span>{{ t("dashboard.outputTokens") }}</span>
          <strong>{{ formatTokenCount(summary.output_tokens) }}</strong>
        </div>
        <div
          class="summary-item cache"
          :title="formatExactTokenCount(summary.cache_read_tokens + summary.cache_write_tokens)"
        >
          <span>{{ t("dashboard.cacheTokens") }}</span>
          <strong>
            {{ formatTokenCount(summary.cache_read_tokens + summary.cache_write_tokens) }}
          </strong>
        </div>
        <div class="summary-item thinking" :title="formatExactTokenCount(summary.thinking_tokens)">
          <span>{{ t("dashboard.thinkingTokens") }}</span>
          <strong>{{ formatTokenCount(summary.thinking_tokens) }}</strong>
        </div>
        <div
          v-if="summary.estimated_tokens > 0"
          class="summary-item estimated"
          :title="formatExactTokenCount(summary.estimated_tokens)"
        >
          <span>{{ t("dashboard.estimatedTokens") }}</span>
          <strong>{{ formatTokenCount(summary.estimated_tokens) }}</strong>
        </div>
      </div>

      <div v-if="topModels.length > 0" class="token-table">
        <div class="token-row token-row-head">
          <span>{{ t("dashboard.model") }}</span>
          <span>{{ t("dashboard.inputTokens") }}</span>
          <span>{{ t("dashboard.outputTokens") }}</span>
          <span>{{ t("dashboard.totalTokens") }}</span>
        </div>
        <div v-for="item in topModels" :key="item.model" class="token-row">
          <span class="model-name" :title="item.model">{{ item.model }}</span>
          <span
            :data-label="t('dashboard.inputTokens')"
            :title="formatExactTokenCount(item.input_tokens)"
          >
            {{ formatTokenCount(item.input_tokens) }}
          </span>
          <span
            :data-label="t('dashboard.outputTokens')"
            :title="formatExactTokenCount(item.output_tokens)"
          >
            {{ formatTokenCount(item.output_tokens) }}
          </span>
          <span
            class="total-cell"
            :data-label="t('dashboard.totalTokens')"
            :title="formatExactTokenCount(item.total_tokens)"
          >
            <strong>{{ formatTokenCount(item.total_tokens) }}</strong>
            <small v-if="item.estimated_tokens > 0">
              {{ t("dashboard.includesEstimated") }}
              {{ formatTokenCount(item.estimated_tokens) }}
            </small>
          </span>
        </div>
      </div>
      <n-empty v-else-if="!loading" size="small" :description="t('dashboard.noTokenUsage')" />
    </n-spin>
  </div>
</template>

<style scoped>
.token-panel {
  padding: 20px;
  border-radius: 16px;
  backdrop-filter: blur(4px);
  border: 1px solid var(--border-color-light);
}

:root:not(.dark) .token-panel {
  background: var(--primary-gradient);
  color: white;
}

:root.dark .token-panel {
  background: linear-gradient(135deg, #525a7a 0%, #424964 100%);
  box-shadow: var(--shadow-md);
  border: 1px solid rgba(139, 157, 245, 0.2);
  color: #e8e8e8;
}

.token-panel-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 12px;
}

.token-title-section {
  flex: 1;
}

.token-panel-controls {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  justify-content: flex-end;
  flex-wrap: wrap;
}

.token-panel-title {
  margin: 0;
  font-size: 24px;
  line-height: 28px;
  font-weight: 600;
  color: white;
}

.token-panel-subtitle {
  margin: 4px 0 0;
  color: rgba(255, 255, 255, 0.8);
  font-size: 14px;
  font-weight: 400;
}

.token-panel-total {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
  color: white;
  font-variant-numeric: tabular-nums;
  min-width: 96px;
}

.token-total-label {
  font-size: 12px;
  line-height: 16px;
  font-weight: 600;
  color: rgba(255, 255, 255, 0.78);
}

.token-panel-total strong {
  font-size: 24px;
  line-height: 28px;
  font-weight: 700;
}

.token-panel-estimated {
  font-size: 12px;
  line-height: 16px;
  font-weight: 500;
  color: rgba(255, 255, 255, 0.8);
}

.token-range-filter,
.token-model-filter {
  max-width: 100%;
}

.token-range-filter {
  width: 200px;
}

.token-model-filter {
  width: 320px;
}

.token-chart-block {
  margin-bottom: 12px;
}

.token-chart-wrapper {
  position: relative;
  display: flex;
  justify-content: center;
}

.token-chart-legend {
  position: absolute;
  top: 8px;
  left: 50%;
  z-index: 10;
  display: flex;
  justify-content: center;
  gap: 12px;
  padding: 2px;
  border-radius: 24px;
  transform: translateX(-50%);
  backdrop-filter: blur(8px);
}

:root:not(.dark) .token-chart-legend {
  background: rgba(255, 255, 255, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.5);
}

:root.dark .token-chart-legend {
  background: var(--overlay-bg);
  border: 1px solid var(--border-color);
}

.token-legend-item {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px 16px;
  border-radius: 20px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

:root:not(.dark) .token-legend-item {
  color: #334155;
  background: rgba(255, 255, 255, 0.62);
  border: 1px solid rgba(255, 255, 255, 0.7);
}

:root.dark .token-legend-item {
  background: var(--bg-tertiary);
  border: 1px solid var(--border-color);
}

.token-legend-indicator {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  border-radius: 3px;
}

.token-legend-label {
  overflow: hidden;
  color: inherit;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.token-legend-total {
  color: inherit;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.token-chart-svg {
  width: 100%;
  height: auto;
  border-radius: 8px;
}

:root:not(.dark) .token-chart-svg {
  background: white;
  border: 1px solid #e0e0e0;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

:root.dark .token-chart-svg {
  background: var(--card-bg-solid);
  border: 1px solid var(--border-color);
  box-shadow: inset 0 2px 4px rgba(0, 0, 0, 0.2);
}

.token-axis-label {
  fill: var(--chart-text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  font-size: 12px;
}

.token-line-path,
.token-area-path {
  transition: opacity 0.2s ease;
}

.token-data-point {
  cursor: pointer;
  transition: all 0.2s ease;
}

.token-data-point:hover,
.token-point-hover {
  r: 5;
  filter: drop-shadow(0 0 6px rgba(0, 0, 0, 0.3));
}

.token-chart-tooltip {
  position: absolute;
  z-index: 1000;
  min-width: 150px;
  max-width: 240px;
  padding: 12px 16px;
  color: white;
  pointer-events: none;
  background: rgba(0, 0, 0, 0.9);
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 8px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
  font-size: 13px;
  transform: translateX(-50%) translateY(-100%);
  backdrop-filter: blur(8px);
}

.token-tooltip-time {
  padding-bottom: 6px;
  margin-bottom: 8px;
  color: #e2e8f0;
  font-size: 12px;
  font-weight: 700;
  text-align: center;
  border-bottom: 1px solid rgba(255, 255, 255, 0.2);
}

.token-tooltip-value {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 4px;
  font-size: 12px;
  font-weight: 600;
}

.token-tooltip-value:last-child {
  margin-bottom: 0;
}

.token-tooltip-color {
  width: 8px;
  height: 8px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.token-summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.summary-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 12px;
  border-radius: var(--border-radius-md);
  border: 1px solid var(--border-color-light);
  background: var(--bg-secondary);
}

:root:not(.dark) .summary-item {
  color: #334155;
  background: rgba(255, 255, 255, 0.72);
  border-color: rgba(255, 255, 255, 0.75);
}

:root.dark .summary-item {
  background: var(--bg-tertiary);
  border-color: var(--border-color);
}

.summary-item span {
  font-size: 12px;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
}

:root:not(.dark) .summary-item span {
  color: #475569;
}

.summary-item strong {
  font-size: 18px;
  line-height: 22px;
  color: var(--text-primary);
  font-variant-numeric: tabular-nums;
}

:root:not(.dark) .summary-item strong {
  color: #1e293b;
}

.summary-item.input {
  border-left: 3px solid #2563eb;
}

.summary-item.output {
  border-left: 3px solid #0f766e;
}

.summary-item.cache {
  border-left: 3px solid #ca8a04;
}

.summary-item.thinking {
  border-left: 3px solid #7c3aed;
}

.summary-item.estimated {
  border-left: 3px solid #64748b;
}

.token-table {
  border: 1px solid var(--border-color-light);
  border-radius: var(--border-radius-md);
  overflow: hidden;
}

:root:not(.dark) .token-table {
  border-color: rgba(255, 255, 255, 0.75);
  background: rgba(255, 255, 255, 0.6);
}

:root.dark .token-table {
  border-color: var(--border-color);
  background: var(--bg-secondary);
}

.token-row {
  display: grid;
  grid-template-columns: minmax(160px, 1.6fr) repeat(3, minmax(84px, 1fr));
  gap: 12px;
  align-items: center;
  padding: 10px 12px;
  border-top: 1px solid var(--border-color-light);
  color: var(--text-primary);
  font-size: 13px;
}

:root:not(.dark) .token-row {
  color: #334155;
  border-top-color: rgba(255, 255, 255, 0.7);
}

.token-row:first-child {
  border-top: none;
}

.token-row-head {
  background: var(--bg-secondary);
  color: var(--text-secondary);
  font-weight: 600;
}

:root:not(.dark) .token-row-head {
  background: rgba(255, 255, 255, 0.56);
  color: #475569;
}

.model-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}

.total-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-weight: 700;
}

.total-cell strong {
  font: inherit;
}

.total-cell small {
  color: var(--text-secondary);
  font-size: 11px;
  line-height: 14px;
  font-weight: 500;
}

:root:not(.dark) .total-cell small {
  color: #64748b;
}

@media (max-width: 768px) {
  .token-panel-header {
    flex-direction: column;
    gap: 12px;
    align-items: flex-start;
  }

  .token-panel-title {
    font-size: 20px;
  }

  .token-panel-controls {
    width: 100%;
  }

  .token-range-filter,
  .token-model-filter {
    width: 100%;
  }

  .token-panel-total {
    margin-left: auto;
  }

  .token-chart-legend {
    position: relative;
    top: auto;
    left: auto;
    flex-wrap: wrap;
    width: 100%;
    margin-top: 8px;
    margin-bottom: 12px;
    background: transparent;
    border: none;
    transform: none;
    backdrop-filter: none;
  }

  :root:not(.dark) .token-legend-item,
  :root.dark .token-legend-item {
    padding: 4px 10px;
    color: #333;
    background: white;
    border: 1px solid rgba(0, 0, 0, 0.1);
    font-size: 12px;
    gap: 6px;
  }

  .token-summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .token-row {
    grid-template-columns: minmax(120px, 1.4fr) repeat(3, minmax(64px, 1fr));
    gap: 8px;
    padding: 9px 10px;
    font-size: 12px;
  }
}

@media (max-width: 520px) {
  .token-summary-grid {
    grid-template-columns: 1fr;
  }

  .token-row {
    grid-template-columns: minmax(0, 1fr);
    gap: 4px;
  }

  .token-row-head {
    display: none;
  }

  .model-name {
    white-space: normal;
  }

  .token-row > span[data-label] {
    display: grid;
    grid-template-columns: auto minmax(0, max-content);
    gap: 12px;
    align-items: start;
  }

  .token-row > span[data-label]::before {
    content: attr(data-label);
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 600;
    line-height: 16px;
  }

  .token-row > span[data-label]:not(.total-cell) {
    justify-content: space-between;
  }

  .total-cell[data-label] {
    grid-template-columns: auto minmax(0, max-content);
  }

  .total-cell[data-label] small {
    grid-column: 2;
    text-align: right;
  }
}

@keyframes fadeInUp {
  from {
    opacity: 0;
    transform: translateY(20px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
