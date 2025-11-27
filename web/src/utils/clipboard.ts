/**
 * Copy text to clipboard with best-effort cross-browser support.
 */
export async function copy(text: string): Promise<boolean> {
  if (!text) {
    return false;
  }

  // Prefer modern async clipboard API when available.
  // navigator.clipboard is typically only exposed in secure contexts (HTTPS or localhost),
  // but some environments may provide it conditionally. Errors are caught and we fall back.
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (e) {
      console.error("Failed to copy text using navigator.clipboard:", e);
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
