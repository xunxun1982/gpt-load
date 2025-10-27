package utils

import (
	"testing"
)

func TestApplyModelMapping(t *testing.T) {
	tests := []struct {
		name          string
		originalModel string
		mappingJSON   string
		wantModel     string
		wantMapped    bool
		wantErr       bool
	}{
		{
			name:          "empty mapping",
			originalModel: "gpt-4",
			mappingJSON:   "",
			wantModel:     "gpt-4",
			wantMapped:    false,
			wantErr:       false,
		},
		{
			name:          "empty object mapping",
			originalModel: "gpt-4",
			mappingJSON:   "{}",
			wantModel:     "gpt-4",
			wantMapped:    false,
			wantErr:       false,
		},
		{
			name:          "simple mapping",
			originalModel: "gpt-4",
			mappingJSON:   `{"gpt-4":"gpt-4-turbo"}`,
			wantModel:     "gpt-4-turbo",
			wantMapped:    true,
			wantErr:       false,
		},
		{
			name:          "no matching mapping",
			originalModel: "gpt-3.5-turbo",
			mappingJSON:   `{"gpt-4":"gpt-4-turbo"}`,
			wantModel:     "gpt-3.5-turbo",
			wantMapped:    false,
			wantErr:       false,
		},
		{
			name:          "chained mapping",
			originalModel: "gpt-4",
			mappingJSON:   `{"gpt-4":"gpt-4-turbo","gpt-4-turbo":"gpt-4-turbo-preview"}`,
			wantModel:     "gpt-4-turbo-preview",
			wantMapped:    true,
			wantErr:       false,
		},
		{
			name:          "self mapping",
			originalModel: "gpt-4",
			mappingJSON:   `{"gpt-4":"gpt-4"}`,
			wantModel:     "gpt-4",
			wantMapped:    false,
			wantErr:       false,
		},
		{
			name:          "circular reference",
			originalModel: "gpt-4",
			mappingJSON:   `{"gpt-4":"gpt-4-turbo","gpt-4-turbo":"gpt-4"}`,
			wantModel:     "",
			wantMapped:    false,
			wantErr:       true,
		},
		{
			name:          "invalid JSON",
			originalModel: "gpt-4",
			mappingJSON:   `{invalid}`,
			wantModel:     "",
			wantMapped:    false,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotMapped, err := ApplyModelMapping(tt.originalModel, tt.mappingJSON)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyModelMapping() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotModel != tt.wantModel {
				t.Errorf("ApplyModelMapping() gotModel = %v, want %v", gotModel, tt.wantModel)
			}
			if gotMapped != tt.wantMapped {
				t.Errorf("ApplyModelMapping() gotMapped = %v, want %v", gotMapped, tt.wantMapped)
			}
		})
	}
}

func TestValidateModelMapping(t *testing.T) {
	tests := []struct {
		name        string
		mappingJSON string
		wantErr     bool
	}{
		{
			name:        "empty mapping",
			mappingJSON: "",
			wantErr:     false,
		},
		{
			name:        "valid mapping",
			mappingJSON: `{"gpt-4":"gpt-4-turbo"}`,
			wantErr:     false,
		},
		{
			name:        "valid chained mapping",
			mappingJSON: `{"gpt-4":"gpt-4-turbo","gpt-4-turbo":"gpt-4-turbo-preview"}`,
			wantErr:     false,
		},
		{
			name:        "circular reference",
			mappingJSON: `{"gpt-4":"gpt-4-turbo","gpt-4-turbo":"gpt-4"}`,
			wantErr:     true,
		},
		{
			name:        "invalid JSON",
			mappingJSON: `{invalid}`,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModelMapping(tt.mappingJSON)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateModelMapping() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseModelMapping(t *testing.T) {
	tests := []struct {
		name        string
		mappingJSON string
		wantNil     bool
		wantErr     bool
	}{
		{
			name:        "empty mapping",
			mappingJSON: "",
			wantNil:     true,
			wantErr:     false,
		},
		{
			name:        "empty object",
			mappingJSON: "{}",
			wantNil:     true,
			wantErr:     false,
		},
		{
			name:        "valid mapping",
			mappingJSON: `{"gpt-4":"gpt-4-turbo"}`,
			wantNil:     false,
			wantErr:     false,
		},
		{
			name:        "invalid JSON",
			mappingJSON: `{invalid}`,
			wantNil:     true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseModelMapping(tt.mappingJSON)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseModelMapping() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("ParseModelMapping() got nil = %v, want nil = %v", got == nil, tt.wantNil)
			}
		})
	}
}
