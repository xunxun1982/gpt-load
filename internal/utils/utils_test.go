package utils

import (
	"bytes"
	"strings"
	"testing"
)

// TestMaskAPIKey tests API key masking functionality
func TestMaskAPIKey(t *testing.T) {
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

// TestWeightedRandomSelect tests weighted random selection
func TestWeightedRandomSelect(t *testing.T) {
	tests := []struct {
		name    string
		weights []int
		wantErr bool
	}{
		{"EmptyWeights", []int{}, true},
		{"AllZeroWeights", []int{0, 0, 0}, true},
		{"SingleWeight", []int{100}, false},
		{"MultipleWeights", []int{100, 200, 300}, false},
		{"WithZeroWeights", []int{100, 0, 200}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WeightedRandomSelect(tt.weights)
			if tt.wantErr {
				if result != -1 {
					t.Errorf("WeightedRandomSelect(%v) = %d, want -1", tt.weights, result)
				}
			} else {
				if result < 0 || result >= len(tt.weights) {
					t.Errorf("WeightedRandomSelect(%v) = %d, out of range", tt.weights, result)
				}
				if tt.weights[result] == 0 {
					t.Errorf("WeightedRandomSelect(%v) selected zero-weight index %d", tt.weights, result)
				}
			}
		})
	}
}

// TestGenerateRandomSuffix tests random suffix generation
func TestGenerateRandomSuffix(t *testing.T) {
	suffix := GenerateRandomSuffix()
	if len(suffix) != 4 {
		t.Errorf("GenerateRandomSuffix() length = %d, want 4", len(suffix))
	}
	// Check that suffix contains only valid characters
	for _, c := range suffix {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Errorf("GenerateRandomSuffix() contains invalid character %c", c)
		}
	}
}

// TestGenerateTriggerSignal tests trigger signal generation
func TestGenerateTriggerSignal(t *testing.T) {
	signal := GenerateTriggerSignal()
	const prefix = "<<CALL_"
	const suffixLen = 6
	const trailer = ">>"
	minLen := len(prefix) + suffixLen + len(trailer)
	if len(signal) < minLen {
		t.Fatalf("GenerateTriggerSignal() length = %d, want >= %d", len(signal), minLen)
	}
	if !strings.HasPrefix(signal, prefix) {
		t.Errorf("GenerateTriggerSignal() = %q, want prefix <<CALL_", signal)
	}
	if !strings.HasSuffix(signal, trailer) {
		t.Errorf("GenerateTriggerSignal() = %q, want suffix >>", signal)
	}
	// Extract suffix and check length
	suffix := signal[len(prefix) : len(signal)-len(trailer)]
	if len(suffix) != suffixLen {
		t.Errorf("GenerateTriggerSignal() suffix length = %d, want 6", len(suffix))
	}
}

// TestGenerateSecureRandomString tests secure random string generation
func TestGenerateSecureRandomString(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{"DefaultLength", 0},
		{"ShortLength", 10},
		{"MediumLength", 32},
		{"LongLength", 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateSecureRandomString(tt.length)
			expectedLen := tt.length
			if expectedLen <= 0 {
				expectedLen = 48
			}
			if len(result) != expectedLen {
				t.Errorf("GenerateSecureRandomString(%d) length = %d, want %d", tt.length, len(result), expectedLen)
			}
			// Check that result contains only valid base64 URL-safe characters
			for _, c := range result {
				valid := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
				if !valid {
					t.Errorf("GenerateSecureRandomString(%d) contains invalid character %c", tt.length, c)
				}
			}
		})
	}
}

// TestValidatePasswordStrength tests password strength validation
func TestValidatePasswordStrength(t *testing.T) {
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

// TestBufferPool tests buffer pool functionality
func TestBufferPool(t *testing.T) {
	t.Run("GetAndPutBuffer", func(t *testing.T) {
		buf := GetBuffer()
		if buf == nil {
			t.Fatal("GetBuffer() returned nil")
		}
		buf.WriteString("test data")
		PutBuffer(buf)

		// Get another buffer and ensure it's reset
		buf2 := GetBuffer()
		if buf2.Len() != 0 {
			t.Errorf("Buffer not reset, length = %d, want 0", buf2.Len())
		}
		PutBuffer(buf2)
	})

	t.Run("PutNilBuffer", func(t *testing.T) {
		// Should not panic
		PutBuffer(nil)
	})

	t.Run("LargeBufferNotPooled", func(t *testing.T) {
		buf := GetBuffer()
		// Write more than maxPooledBufferSize
		largeData := make([]byte, 65*1024)
		buf.Write(largeData)
		PutBuffer(buf)
		// Buffer should be discarded, not returned to pool
	})
}

// TestByteSlicePool tests byte slice pool functionality
func TestByteSlicePool(t *testing.T) {
	t.Run("GetAndPutByteSlice", func(t *testing.T) {
		slice := GetByteSlice()
		if slice == nil {
			t.Fatal("GetByteSlice() returned nil")
		}
		*slice = append(*slice, []byte("test")...)
		PutByteSlice(slice)

		// Get another slice and ensure it's reset
		slice2 := GetByteSlice()
		if len(*slice2) != 0 {
			t.Errorf("Slice not reset, length = %d, want 0", len(*slice2))
		}
		PutByteSlice(slice2)
	})

	t.Run("PutNilSlice", func(t *testing.T) {
		// Should not panic
		PutByteSlice(nil)
	})
}

// TestJSONEncoder tests JSON encoder pool functionality
func TestJSONEncoder(t *testing.T) {
	t.Run("EncodeSimpleObject", func(t *testing.T) {
		enc := GetJSONEncoder()
		defer PutJSONEncoder(enc)

		data := map[string]string{"key": "value"}
		result, err := enc.Encode(data)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		expected := `{"key":"value"}`
		if string(result) != expected {
			t.Errorf("Encode() = %q, want %q", string(result), expected)
		}
	})

	t.Run("EncodeMultipleTimes", func(t *testing.T) {
		enc := GetJSONEncoder()
		defer PutJSONEncoder(enc)

		data1 := map[string]int{"a": 1}
		result1, err := enc.Encode(data1)
		if err != nil {
			t.Fatalf("First Encode() error = %v", err)
		}
		expected1 := `{"a":1}`
		if string(result1) != expected1 {
			t.Errorf("First Encode() = %q, want %q", string(result1), expected1)
		}

		data2 := map[string]int{"b": 2}
		result2, err := enc.Encode(data2)
		if err != nil {
			t.Fatalf("Second Encode() error = %v", err)
		}
		expected2 := `{"b":2}`
		if string(result2) != expected2 {
			t.Errorf("Second Encode() = %q, want %q", string(result2), expected2)
		}
	})

	t.Run("PutNilEncoder", func(t *testing.T) {
		// Should not panic
		PutJSONEncoder(nil)
	})
}

// TestStringBuilderPool tests string builder pool functionality
func TestStringBuilderPool(t *testing.T) {
	t.Run("GetAndPutStringBuilder", func(t *testing.T) {
		sb := GetStringBuilder()
		if sb == nil {
			t.Fatal("GetStringBuilder() returned nil")
		}
		sb.WriteString("test")
		PutStringBuilder(sb)

		// Get another builder and ensure it's reset
		sb2 := GetStringBuilder()
		if sb2.Len() != 0 {
			t.Errorf("StringBuilder not reset, length = %d, want 0", sb2.Len())
		}
		PutStringBuilder(sb2)
	})

	t.Run("PutNilStringBuilder", func(t *testing.T) {
		// Should not panic
		PutStringBuilder(nil)
	})
}

// TestMarshalJSON tests pooled JSON marshaling
func TestMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{"SimpleMap", map[string]string{"key": "value"}, `{"key":"value"}`},
		{"Array", []int{1, 2, 3}, `[1,2,3]`},
		{"Struct", struct {
			Name string `json:"name"`
			Age  int    `json:"age"`
		}{"John", 30}, `{"name":"John","age":30}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MarshalJSON(tt.input)
			if err != nil {
				t.Fatalf("MarshalJSON() error = %v", err)
			}
			if string(result) != tt.expected {
				t.Errorf("MarshalJSON() = %q, want %q", string(result), tt.expected)
			}
		})
	}
}
