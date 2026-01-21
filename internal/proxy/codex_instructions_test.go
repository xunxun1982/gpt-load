package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexDefaultInstructions(t *testing.T) {
	assert.NotEmpty(t, codexDefaultInstructions)
	assert.Contains(t, codexDefaultInstructions, "Codex")
	assert.Contains(t, codexDefaultInstructions, "Core Principles")
	assert.Contains(t, codexDefaultInstructions, "Capabilities")
	assert.Contains(t, codexDefaultInstructions, "Guidelines")
	assert.Contains(t, codexDefaultInstructions, "Output")

	// Verify it's concise (less than 1000 characters)
	assert.Less(t, len(codexDefaultInstructions), 1000)
}

func TestCodexOfficialInstructions(t *testing.T) {
	assert.NotEmpty(t, CodexOfficialInstructions)
	assert.Contains(t, CodexOfficialInstructions, "GPT-5.2")
	assert.Contains(t, CodexOfficialInstructions, "Codex CLI")
	assert.Contains(t, CodexOfficialInstructions, "apply_patch")
	assert.Contains(t, CodexOfficialInstructions, "AGENTS.md")

	// Verify it's comprehensive (more than 5000 characters)
	assert.Greater(t, len(CodexOfficialInstructions), 5000)
}

func TestCodexInstructionsDifference(t *testing.T) {
	// Default should be much shorter than official
	assert.Less(t, len(codexDefaultInstructions), len(CodexOfficialInstructions)/10)

	// Both should be valid strings
	assert.NotEmpty(t, strings.TrimSpace(codexDefaultInstructions))
	assert.NotEmpty(t, strings.TrimSpace(CodexOfficialInstructions))
}

func TestCodexInstructionsFormat(t *testing.T) {
	// Default instructions should have proper markdown structure
	assert.True(t, strings.Contains(codexDefaultInstructions, "##"))
	assert.True(t, strings.Contains(codexDefaultInstructions, "-"))

	// Official instructions should have proper markdown structure
	assert.True(t, strings.Contains(CodexOfficialInstructions, "#"))
	assert.True(t, strings.Contains(CodexOfficialInstructions, "##"))
	assert.True(t, strings.Contains(CodexOfficialInstructions, "-"))
}

func TestCodexInstructionsKeywords(t *testing.T) {
	// Test that key concepts are present in default instructions
	keywords := []string{
		"Codex",
		"precise",
		"safe",
		"helpful",
		"apply_patch",
		"AGENTS.md",
	}

	for _, keyword := range keywords {
		assert.Contains(t, codexDefaultInstructions, keyword,
			"Default instructions should contain keyword: %s", keyword)
	}

	// Test that key concepts are present in official instructions
	officialKeywords := []string{
		"GPT-5.2",
		"Codex CLI",
		"apply_patch",
		"update_plan",
		"AGENTS.md",
		"sandbox",
	}

	for _, keyword := range officialKeywords {
		assert.Contains(t, CodexOfficialInstructions, keyword,
			"Official instructions should contain keyword: %s", keyword)
	}
}
