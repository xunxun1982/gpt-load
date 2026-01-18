package utils

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// BuildToolNameShortMap builds a map of original names to shortened names,
// ensuring uniqueness within the request. This is necessary because multiple
// tools may have the same shortened name after truncation.
// Duplicate original names are skipped to prevent map overwrite issues.
//
// Fast path optimization: if all names are already <=limit chars
// and collision-free, we still build the map but avoid expensive shortening logic.
// The map is always returned for consistent downstream handling.
//
// limit: maximum length for tool names (e.g., 64 for OpenAI/Codex/Gemini APIs)
func BuildToolNameShortMap(names []string, limit int) map[string]string {
	if len(names) == 0 {
		return nil
	}

	// Guard against non-positive limits to avoid panics
	if limit <= 0 {
		limit = 1
	}

	// Fast path: check if any name needs shortening
	needsShortening := false
	for _, n := range names {
		if len(n) > limit {
			needsShortening = true
			break
		}
	}

	// If no shortening needed, build identity map directly (fast path)
	if !needsShortening {
		result := make(map[string]string, len(names))
		seen := make(map[string]struct{}, len(names))
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue // Skip duplicates
			}
			seen[n] = struct{}{}
			result[n] = n
		}
		return result
	}

	// Slow path: need to shorten some names
	used := make(map[string]struct{}, len(names))
	result := make(map[string]string, len(names))
	seenOrig := make(map[string]struct{}, len(names))

	// Helper to get base candidate name
	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		// Special handling for MCP tool names (mcp__server__tool format)
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					return cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	// Helper to make name unique by appending suffix.
	// Ensure candidate length never exceeds limit to prevent infinite loops and API rejections.
	makeUnique := func(cand string) string {
		// Guard against empty candidate to avoid invalid tool names like "_1"
		if cand == "" {
			cand = "tool"
		}
		// Short-circuit for tiny limits where suffix-based uniqueness is impossible
		if limit < 3 {
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; i < 1000; i++ {
			suffix := "_" + fmt.Sprintf("%d", i)
			// Truncate suffix if it exceeds limit
			if len(suffix) >= limit {
				suffix = suffix[len(suffix)-(limit-1):]
			}
			allowed := limit - len(suffix)
			// Ensure allowed is non-negative
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			candidate := tmp + suffix
			// Ensure final candidate never exceeds limit
			if len(candidate) > limit {
				candidate = candidate[:limit]
			}
			if _, ok := used[candidate]; !ok {
				return candidate
			}
		}
		// Use UUID suffix if 1000 iterations exhausted to guarantee uniqueness.
		// This should never happen in practice but provides a robust fallback.
		// Loop until unique name found (UUID collision probability ~10^-18 per attempt).
		for {
			suffix := "_" + uuid.New().String()[:8]
			// Truncate suffix if it exceeds limit
			if len(suffix) >= limit {
				suffix = suffix[len(suffix)-(limit-1):]
			}
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			candidate := tmp + suffix
			// Ensure final candidate never exceeds limit
			if len(candidate) > limit {
				candidate = candidate[:limit]
			}
			if _, ok := used[candidate]; !ok {
				return candidate
			}
		}
	}

	for _, n := range names {
		// Skip duplicate original names to prevent map overwrite
		if _, ok := seenOrig[n]; ok {
			continue
		}
		seenOrig[n] = struct{}{}
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		result[n] = uniq
	}
	return result
}

// BuildReverseToolNameMap builds a reverse map from shortened to original names.
// This is used to restore original tool names in responses.
func BuildReverseToolNameMap(shortMap map[string]string) map[string]string {
	reverse := make(map[string]string, len(shortMap))
	for orig, short := range shortMap {
		reverse[short] = orig
	}
	return reverse
}
