package observability_test

import (
	"testing"

	"test-1/observability"
)

// TestStringAttr tests the String attribute helper.
func TestStringAttr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "simple string",
			key:   "user_id",
			value: "123",
		},
		{
			name:  "empty value",
			key:   "status",
			value: "",
		},
		{
			name:  "special characters",
			key:   "message",
			value: "Hello\nWorld\t!",
		},
		{
			name:  "unicode",
			key:   "name",
			value: "José",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := observability.String(tt.key, tt.value)

			if attr.Key != tt.key {
				t.Errorf("Key: got %q, want %q", attr.Key, tt.key)
			}

			if strVal, ok := attr.Value.(string); !ok || strVal != tt.value {
				t.Errorf("Value: got %v, want %q", attr.Value, tt.value)
			}
		})
	}
}

// TestInt64Attr tests the Int64 attribute helper.
func TestInt64Attr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value int64
	}{
		{
			name:  "positive number",
			key:   "order_id",
			value: 12345,
		},
		{
			name:  "zero",
			key:   "count",
			value: 0,
		},
		{
			name:  "negative number",
			key:   "delta",
			value: -100,
		},
		{
			name:  "large number",
			key:   "big_value",
			value: 9223372036854775807, // max int64
		},
		{
			name:  "min value",
			key:   "min_value",
			value: -9223372036854775808, // min int64
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := observability.Int64(tt.key, tt.value)

			if attr.Key != tt.key {
				t.Errorf("Key: got %q, want %q", attr.Key, tt.key)
			}

			if intVal, ok := attr.Value.(int64); !ok || intVal != tt.value {
				t.Errorf("Value: got %v, want %d", attr.Value, tt.value)
			}
		})
	}
}

// TestBoolAttr tests the Bool attribute helper.
func TestBoolAttr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value bool
	}{
		{
			name:  "true value",
			key:   "is_admin",
			value: true,
		},
		{
			name:  "false value",
			key:   "is_deleted",
			value: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := observability.Bool(tt.key, tt.value)

			if attr.Key != tt.key {
				t.Errorf("Key: got %q, want %q", attr.Key, tt.key)
			}

			if boolVal, ok := attr.Value.(bool); !ok || boolVal != tt.value {
				t.Errorf("Value: got %v, want %v", attr.Value, tt.value)
			}
		})
	}
}

// TestAttrTypes tests that different attr types work together.
func TestAttrTypes(t *testing.T) {
	t.Parallel()

	attrs := []observability.Attr{
		observability.String("name", "John"),
		observability.Int64("age", 30),
		observability.Bool("active", true),
		observability.String("email", "john@example.com"),
		observability.Int64("score", 95),
	}

	if len(attrs) != 5 {
		t.Errorf("Expected 5 attrs, got %d", len(attrs))
	}

	// Verify all attrs are properly formed
	expectedKeys := []string{"name", "age", "active", "email", "score"}
	for i, expectedKey := range expectedKeys {
		if attrs[i].Key != expectedKey {
			t.Errorf("Attr %d key: got %q, want %q", i, attrs[i].Key, expectedKey)
		}
	}
}

// TestAttrStruct tests the Attr struct directly.
func TestAttrStruct(t *testing.T) {
	t.Parallel()

	attr := observability.Attr{
		Key:   "custom_key",
		Value: "custom_value",
	}

	if attr.Key != "custom_key" {
		t.Errorf("Key: got %q, want custom_key", attr.Key)
	}

	if attr.Value != "custom_value" {
		t.Errorf("Value: got %v, want custom_value", attr.Value)
	}
}

// TestAttrWithCustomTypes tests attrs with custom value types.
func TestAttrWithCustomTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value interface{}
	}{
		{
			name:  "float",
			key:   "temperature",
			value: 98.6,
		},
		{
			name:  "slice",
			key:   "tags",
			value: []string{"tag1", "tag2"},
		},
		{
			name:  "map",
			key:   "metadata",
			value: map[string]interface{}{"key": "value"},
		},
		{
			name:  "nil",
			key:   "optional",
			value: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := observability.Attr{
				Key:   tt.key,
				Value: tt.value,
			}

			if attr.Key != tt.key {
				t.Errorf("Key: got %q, want %q", attr.Key, tt.key)
			}

			if attr.Value != tt.value {
				t.Errorf("Value mismatch for type %T", tt.value)
			}
		})
	}
}
