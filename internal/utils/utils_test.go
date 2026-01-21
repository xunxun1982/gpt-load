package utils

import (
	"bytes"
	"testing"
)

// TestMaskAPIKey tests API key masking functionality
func TestMaskAPIKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{"ShortKey", "sk-123", "sk-123"},
		{"ExactlyEightChars", "sk-12345", "sk-12345"},
		{"NormalKey", "sk-1234567890abcdef", "sk-1****cdef"},
		{"LongKey", "sk-proj-1234567890abcdefghijklmnopqrstuvwxyz", "sk-p****wxyz"},
		{"EmptyKey", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskAPIKey(tt.key)
			if result != tt.expected {
				t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.key, result, tt.expected)
			}
		})
	}
}

// TestTruncateString tests string truncation with UTF-8 awareness
func TestTruncateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		maxLength int
		expected  string
	}{
		{"EmptyString", "", 10, ""},
		{"ZeroLength", "hello", 0, ""},
		{"NegativeLength", "hello", -1, ""},
		{"NoTruncation", "hello", 10, "hello"},
		{"ExactLength", "hello", 5, "hello"},
		{"SimpleTruncation", "hello world", 5, "hello"},
		{"UTF8Truncation", "你好世界", 2, "你好"},
		{"MixedUTF8", "Hello世界", 7, "Hello世界"},
		{"EmojiTruncation", "Hello🌍World", 6, "Hello🌍"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateString(tt.input, tt.maxLength)
			if result != tt.expected {
				t.Errorf("TruncateString(%q, %d) = %q, want %q", tt.input, tt.maxLength, result, tt.expected)
			}
		})
	}
}

// TestSplitAndTrim tests string splitting and trimming
func TestSplitAndTrim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		sep      string
		expected []string
	}{
		{"EmptyString", "", ",", []string{}},
		{"SingleItem", "item1", ",", []string{"item1"}},
		{"MultipleItems", "item1,item2,item3", ",", []string{"item1", "item2", "item3"}},
		{"WithSpaces", " item1 , item2 , item3 ", ",", []string{"item1", "item2", "item3"}},
		{"EmptyItems", "item1,,item2", ",", []string{"item1", "item2"}},
		{"OnlySpaces", "  ,  ,  ", ",", []string{}},
		{"DifferentSeparator", "a|b|c", "|", []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SplitAndTrim(tt.input, tt.sep)
			if len(result) != len(tt.expected) {
				t.Errorf("SplitAndTrim(%q, %q) length = %d, want %d", tt.input, tt.sep, len(result), len(tt.expected))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("SplitAndTrim(%q, %q)[%d] = %q, want %q", tt.input, tt.sep, i, result[i], tt.expected[i])
				}
			}
		})
	}
}

// TestStringToSet tests string to set conversion
func TestStringToSet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		sep      string
		expected map[string]struct{}
	}{
		{"EmptyString", "", ",", nil},
		{"SingleItem", "item1", ",", map[string]struct{}{"item1": {}}},
		{"MultipleItems", "a,b,c", ",", map[string]struct{}{"a": {}, "b": {}, "c": {}}},
		{"WithDuplicates", "a,b,a,c", ",", map[string]struct{}{"a": {}, "b": {}, "c": {}}},
		{"WithSpaces", " a , b , c ", ",", map[string]struct{}{"a": {}, "b": {}, "c": {}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StringToSet(tt.input, tt.sep)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("StringToSet(%q, %q) = %v, want nil", tt.input, tt.sep, result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("StringToSet(%q, %q) length = %d, want %d", tt.input, tt.sep, len(result), len(tt.expected))
				return
			}
			for key := range tt.expected {
				if _, ok := result[key]; !ok {
					t.Errorf("StringToSet(%q, %q) missing key %q", tt.input, tt.sep, key)
				}
			}
		})
	}
}

// TestValidatePasswordStrength tests password strength validation
func TestValidatePasswordStrength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		password  string
		fieldName string
	}{
		{"StrongPassword", "ThisIsAVeryStrongPassword123!", "AUTH_KEY"},
		{"ShortPassword", "short", "AUTH_KEY"},
		{"WeakPattern", "password123456789", "AUTH_KEY"},
		{"CommonPattern", "admin1234567890", "AUTH_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This function only logs warnings, so we just ensure it doesn't panic
			ValidatePasswordStrength(tt.password, tt.fieldName)
		})
	}
}

// TestDeriveAESKey tests AES key derivation
func TestDeriveAESKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
	}{
		{"SimplePassword", "password123"},
		{"ComplexPassword", "ThisIsAVeryComplexPassword!@#$%^&*()"},
		{"EmptyPassword", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := DeriveAESKey(tt.password)
			if len(key) != 32 {
				t.Errorf("DeriveAESKey(%q) length = %d, want 32", tt.password, len(key))
			}
			// Ensure same password produces same key
			key2 := DeriveAESKey(tt.password)
			if !bytes.Equal(key, key2) {
				t.Errorf("DeriveAESKey(%q) not deterministic", tt.password)
			}
		})
	}
}
