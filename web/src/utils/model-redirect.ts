export interface ModelRedirectTargetItem {
  model: string;
  weight: number;
  enabled: boolean;
}

export interface ModelRedirectItemV2 {
  from: string;
  targets: ModelRedirectTargetItem[];
}

type SerializedModelRedirectTarget = {
  model: string;
  weight?: number;
  enabled?: boolean;
};

type SerializedModelRedirectRule = {
  targets: SerializedModelRedirectTarget[];
};

function modelRedirectItemsV2ToObject(
  items: ModelRedirectItemV2[] | undefined
): Record<string, SerializedModelRedirectRule> {
  if (!items || items.length === 0) {
    return {};
  }

  const obj: Record<string, SerializedModelRedirectRule> = {};
  items.forEach(item => {
    if (item.from.trim()) {
      const validTargets = item.targets
        .filter(t => t.model.trim())
        .map(t => ({
          model: t.model.trim(),
          weight: t.weight !== 100 ? t.weight : undefined,
          enabled: t.enabled === false ? false : undefined,
        }));
      if (validTargets.length > 0) {
        obj[item.from.trim()] = { targets: validTargets };
      }
    }
  });
  return obj;
}

export function modelRedirectItemsV2ToJson(items: ModelRedirectItemV2[]): string {
  const obj = modelRedirectItemsV2ToObject(items);
  return Object.keys(obj).length > 0 ? JSON.stringify(obj) : "";
}

export function modelRedirectItemsV2ToFormattedJson(items: ModelRedirectItemV2[]): string {
  return JSON.stringify(modelRedirectItemsV2ToObject(items), null, 2);
}

export function parseJsonToModelRedirectItemsV2(jsonStr: string): ModelRedirectItemV2[] {
  if (!jsonStr || jsonStr.trim() === "" || jsonStr.trim() === "{}") {
    return [];
  }
  try {
    const obj = JSON.parse(jsonStr) as Record<
      string,
      { targets?: Array<{ model: string; weight?: number; enabled?: boolean }> }
    >;
    const items: ModelRedirectItemV2[] = [];
    for (const [from, ruleObj] of Object.entries(obj)) {
      if (ruleObj.targets && Array.isArray(ruleObj.targets)) {
        items.push({
          from,
          targets: ruleObj.targets.map(t => ({
            model: t.model || "",
            weight: t.weight ?? 100,
            enabled: t.enabled !== false,
          })),
        });
      }
    }
    return mergeModelRedirectItems(items);
  } catch {
    return [];
  }
}

export function mergeModelRedirectItems(items: ModelRedirectItemV2[]): ModelRedirectItemV2[] {
  if (!items || items.length === 0) {
    return items;
  }

  const mergedMap = new Map<string, ModelRedirectItemV2>();

  for (const item of items) {
    const from = item.from.trim();
    if (!from) {
      continue;
    }

    if (mergedMap.has(from)) {
      const existing = mergedMap.get(from);
      if (!existing) {
        continue;
      }
      const seenModels = new Set(existing.targets.map(t => t.model.trim()));

      for (const target of item.targets) {
        const model = target.model.trim();
        if (model && !seenModels.has(model)) {
          existing.targets.push({ ...target, model });
          seenModels.add(model);
        }
      }
    } else {
      const seenModels = new Set<string>();
      const uniqueTargets: ModelRedirectTargetItem[] = [];

      for (const target of item.targets) {
        const model = target.model.trim();
        if (model && !seenModels.has(model)) {
          uniqueTargets.push({ ...target, model });
          seenModels.add(model);
        }
      }

      if (uniqueTargets.length > 0) {
        mergedMap.set(from, {
          from,
          targets: uniqueTargets,
        });
      }
    }
  }

  return Array.from(mergedMap.values());
}

export function hasEffectiveModelRedirectItems(items: ModelRedirectItemV2[]): boolean {
  // Keep notice visibility aligned with the same normalized payload saved on submit.
  return modelRedirectItemsV2ToJson(mergeModelRedirectItems(items)) !== "";
}

export function hasEffectiveModelRedirectJson(jsonStr: string): boolean {
  return modelRedirectItemsV2ToJson(parseJsonToModelRedirectItemsV2(jsonStr)) !== "";
}
