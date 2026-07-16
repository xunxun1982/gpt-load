package sitemanagement

import "testing"

func TestScaledManagedSiteBalance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		balance    string
		multiplier int64
		want       string
	}{
		{name: "empty", balance: "", multiplier: 3, want: ""},
		{name: "default multiplier", balance: "$120.00", multiplier: 1, want: "$120.00"},
		{name: "legacy zero multiplier", balance: "$120.00", multiplier: 0, want: "$120.00"},
		{name: "exact division", balance: "$120.00", multiplier: 3, want: "$40.00"},
		{name: "rounded division", balance: "$1.00", multiplier: 3, want: "$0.33"},
		{name: "negative balance", balance: "$-1.00", multiplier: 2, want: "$-0.50"},
		{name: "unknown legacy value", balance: "unknown", multiplier: 5, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := scaledManagedSiteBalance(tt.balance, tt.multiplier); got != tt.want {
				t.Fatalf("scaledManagedSiteBalance(%q, %d) = %q, want %q", tt.balance, tt.multiplier, got, tt.want)
			}
		})
	}
}
