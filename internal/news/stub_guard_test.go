package news

import (
	"testing"

	"github.com/deusflow/News/internal/ai"
)

func TestIsStubOrApology(t *testing.T) {
	tests := []struct {
		name     string
		resp     *ai.Response
		wantStub bool
	}{
		{
			name:     "Nil response is considered stub",
			resp:     nil,
			wantStub: true,
		},
		{
			name: "Flagged with insufficient_substance concrete anchor",
			resp: &ai.Response{
				ConcreteAnchor: "insufficient_substance",
				Danish:         "En artikel er udgivet.",
				Ukrainian:      "Стаття опублікована.",
			},
			wantStub: true,
		},
		{
			name: "Flagged with Ukrainian lack of substance anchor",
			resp: &ai.Response{
				ConcreteAnchor: "джерело не містить конкретики",
				Danish:         "En artikel er udgivet.",
				Ukrainian:      "Стаття опублікована.",
			},
			wantStub: true,
		},
		{
			name: "The Kristeligt Dagblad incident phrasing in Danish",
			resp: &ai.Response{
				ConcreteAnchor: "pension 2035",
				Danish:         "Kristeligt Dagblad har udgivet en artikel. Yderligere detaljer er ikke tilgængelige i det åbne kildemateriale.",
				Ukrainian:      "Часопис опублікував новину про пенсію.",
			},
			wantStub: true,
		},
		{
			name: "The Kristeligt Dagblad incident phrasing in Ukrainian",
			resp: &ai.Response{
				ConcreteAnchor: "pension 2035",
				Danish:         "Kristeligt Dagblad har udgivet en artikel om pension.",
				Ukrainian:      "Проте першоджерело не містить додаткових конкретних деталей у відкритому доступі.",
			},
			wantStub: true,
		},
		{
			name: "Valid news item passes cleanly",
			resp: &ai.Response{
				ConcreteAnchor: "3.200 til 4.100 kr.",
				Danish:         "Boligstøtten stiger fra 3.200 til 4.100 kr. om måneden fra 1. januar for 40.000 modtagere.",
				Ukrainian:      "Субсидія на житло в Данії зросте з 3.200 до 4.100 крон на місяць з 1 січня.",
			},
			wantStub: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStubOrApology(tt.resp)
			if got != tt.wantStub {
				t.Errorf("isStubOrApology() = %v, want %v", got, tt.wantStub)
			}
		})
	}
}
