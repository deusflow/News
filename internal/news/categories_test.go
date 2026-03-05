package news

import "testing"

func TestCoerceCategory_Direct(t *testing.T) {
	c, ok := CoerceCategory("economy")
	if !ok || c != CategoryEconomy {
		t.Fatalf("expected direct category economy, got c=%q ok=%v", c, ok)
	}
}

func TestCoerceCategory_Alias(t *testing.T) {
	tests := []struct {
		in   string
		want Category
	}{
		{in: "housing", want: CategorySociety},
		{in: "health", want: CategorySociety},
		{in: "transport", want: CategoryLocal},
	}

	for _, tt := range tests {
		got, ok := CoerceCategory(tt.in)
		if !ok || got != tt.want {
			t.Fatalf("alias %q -> got=%q ok=%v, want=%q", tt.in, got, ok, tt.want)
		}
	}
}

func TestCoerceCategory_Unknown(t *testing.T) {
	c, ok := CoerceCategory("unknown-category")
	if ok {
		t.Fatalf("expected unknown category to be invalid, got ok=true c=%q", c)
	}
	if c != CategoryDefault {
		t.Fatalf("expected default category for unknown, got %q", c)
	}
}
