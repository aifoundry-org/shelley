package main

import "testing"

func TestParseLoginArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantProv string
		wantErr  bool
	}{
		{"anthropic", []string{"anthropic"}, "anthropic", false},
		{"no args", []string{}, "", true},
		{"unsupported provider", []string{"openai"}, "", true},
		{"too many", []string{"anthropic", "extra"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov, err := parseLoginArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %v", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prov != tt.wantProv {
				t.Errorf("provider = %q, want %q", prov, tt.wantProv)
			}
		})
	}
}
