package slugify

import "testing"

func TestSlug_NormalizesWhitespaceAndNewlines(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "newline in headline becomes hyphen",
			title: "Ecco fyrer\n76 i Danmark",
			want:  "ecco-fyrer-76-i-danmark",
		},
		{
			name:  "tabs and multiple newlines collapse",
			title: "Skoleledere:\t\n  Forældre\r\nskal hjælpe",
			want:  "skoleledere-foraeldre-skal-hjaelpe",
		},
		{
			name:  "ukrainian transliteration",
			title: "Сєрлов допоможе — але не всім",
			want:  "syerlov-dopomozhe-ale-ne-vsim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slug(tt.title); got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}
