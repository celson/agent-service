package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseInput(t *testing.T) {
	type TestStruct struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	tests := []struct {
		name        string
		input       json.RawMessage
		wantErr     bool
		errContains string
		wantData    *TestStruct
	}{
		{
			name:        "valid json",
			input:       json.RawMessage(`{"name": "test", "value": 123}`),
			wantErr:     false,
			errContains: "",
			wantData:    &TestStruct{Name: "test", Value: 123},
		},
		{
			name:        "invalid json",
			input:       json.RawMessage(`{"name": "test", "value": 123`),
			wantErr:     true,
			errContains: "parse tool input",
			wantData:    nil,
		},
		{
			name:        "type mismatch",
			input:       json.RawMessage(`{"name": "test", "value": "123"}`),
			wantErr:     true,
			errContains: "parse tool input",
			wantData:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInput[TestStruct](tt.input)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseInput() error = nil, wantErr %v", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("ParseInput() error = %v, errContains %v", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseInput() unexpected error = %v", err)
			}

			if got.Name != tt.wantData.Name || got.Value != tt.wantData.Value {
				t.Errorf("ParseInput() got = %v, want %v", got, *tt.wantData)
			}
		})
	}
}
