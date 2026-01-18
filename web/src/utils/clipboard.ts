/**
 * Copy text to clipboard with best-effort cross-browser support.
 * Returns a promise that resolves to true if successful, false otherwise.
 */
export async function copy(text: string): Promise<boolean> {
  if (!text) {
    return false;
  }

  // Prefer modern async clipboard API when available.
  // navigator.clipboard is typically only exposed in secure contexts (HTTPS or localhost),
  // but some environments may provide it conditionally. Errors are caught and we fall back.
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (e) {
      console.error("Failed to copy text using navigator.clipboard:", e);
      // Fall through to execCommand fallback
    }
  }

  // Fallback: use the deprecated execCommand API with an off-screen textarea.
  // This approach is widely used and works in more browsers and HTTP contexts.
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.top = "0";
    textarea.style.left = "-9999px";
    textarea.style.opacity = "0";

    if (!document.body) {
      console.error("document.body is not available for clipboard copy");
      return false;
    }

    document.body.appendChild(textarea);

    const selection = document.getSelection();
    const originalRange = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;

    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length); // For mobile devices

    const result = document.execCommand("copy");

    document.body.removeChild(textarea);

    if (selection) {
      selection.removeAllRanges();
      if (originalRange) {
        selection.addRange(originalRange);
      }
    }

    if (!result) {
      console.error("Failed to copy text using document.execCommand");
      return false;
    }
    return true;
  } catch (e) {
    console.error("Fallback copy method threw an error:", e);
    return false;
  }
}

/**
 * Copy text to clipboard with manual fallback dialog.
 * If automatic copy fails, shows a dialog for manual copying.
 * This provides a manual-copy fallback when automatic copy fails.
 */
export async function copyWithFallback(
  text: string,
  options: {
    onSuccess?: () => void;
    onError?: () => void;
    showManualDialog?: (text: string) => void;
  } = {}
): Promise<boolean> {
  const success = await copy(text);

  if (success) {
    options.onSuccess?.();
    return true;
  } else {
    options.onError?.();
    // If copy failed and manual dialog callback is provided, show it
    if (options.showManualDialog) {
      options.showManualDialog(text);
    }
    return false;
  }
}

/**
 * Create a manual copy dialog content using Naive UI's h function.
 * This is a helper to create consistent manual copy dialogs across the app.
 *
 * @param h - Vue's h function for creating VNodes
 * @param text - Text to display for manual copying
 * @param t - i18n translate function
 * @returns VNode for dialog content
 */
export function createManualCopyContent(h: any, text: string, t: (key: string) => string) {
  return h("div", { style: "word-break: break-all;" }, [
    h("p", { style: "margin-bottom: 12px;" }, t("keys.copyFailedManual")),
    h(
      "code",
      {
        style:
          "display: block; padding: 12px; background: var(--n-color-embedded); border-radius: 4px; user-select: all; font-family: monospace; word-break: break-all; white-space: pre-wrap;",
      },
      text
    ),
    h(
      "p",
      {
        style: "margin-top: 12px; font-size: 12px; color: var(--n-text-color-3);",
      },
      t("keys.manualCopyHint")
    ),
  ]);
}
