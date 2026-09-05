package scraper

import (
	"testing"
)

func TestIsPaywalledOrStub(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		url          string
		wantPaywall  bool
		reasonSubstr string
	}{
		{
			name:         "Empty content is rejected",
			content:      "",
			url:          "https://www.dr.dk/nyheder/indland/test",
			wantPaywall:  true,
			reasonSubstr: "empty content",
		},
		{
			name:         "Whitespace only is rejected",
			content:      "   \n\t  ",
			url:          "https://www.dr.dk/nyheder/indland/test",
			wantPaywall:  true,
			reasonSubstr: "empty content",
		},
		{
			name:         "Explicit paywall phrase: kræver abonnement",
			content:      "Regeringen præsenterer nyt forslag i dag. Denne artikel kræver abonnement på Politiken for at læse videre. Prøv i dag.",
			url:          "https://politiken.dk/danmark/art12345/regeringen-forslag",
			wantPaywall:  true,
			reasonSubstr: "kræver abonnement",
		},
		{
			name:         "Explicit paywall phrase: artiklen er forbeholdt abonnenter",
			content:      "Dansk økonomi vokser hurtigere end ventet ifølge nye tal. Artiklen er forbeholdt abonnenter på Berlingske.",
			url:          "https://www.berlingske.dk/oekonomi/dansk-oekonomi-vokser",
			wantPaywall:  true,
			reasonSubstr: "forbeholdt abonnenter",
		},
		{
			name:         "Explicit paywall phrase: plus-artikel",
			content:      "Nye regler for skat på elbiler træder i kraft til sommer. Dette er en plus-artikel. Bliv abonnent og få adgang.",
			url:          "https://jyllands-posten.dk/politik/nye-regler-skat",
			wantPaywall:  true,
			reasonSubstr: "plus-artikel",
		},
		{
			name:         "Stub content too short (< 200 runes)",
			content:      "Kort nyhed med alt for lidt indhold til at udgøre en reel artikel.",
			url:          "https://www.dr.dk/nyheder/kort",
			wantPaywall:  true,
			reasonSubstr: "stub content too short",
		},
		{
			name: "Berlingske single teaser paragraph with no real body",
			content: "Regeringen og arbejdsmarkedets parter har indgået en ny trepartsaftale om integration og sprogundervisning for udenlandske arbejdstagere i Danmark, erfarer Berlingske fredag eftermiddag.",
			url: "https://www.berlingske.dk/politik/trepartsaftale-integration",
			wantPaywall: true,
			reasonSubstr: "paywalled publisher",
		},
		{
			name: "Valid full article from DR (Danmarks Radio) passes cleanly",
			content: `Regeringen og et bredt flertal i Folketinget er fredag blevet enige om en ny aftale, der skal styrke indsatsen for udenlandske arbejdstagere i Danmark.

Aftalen indeholder blandt andet krav om hurtigere sagsbehandling hos udlændingemyndighederne samt udvidede muligheder for gratis danskundervisning i aftentimerne.

Ifølge beskæftigelsesministeren er formålet at sikre, at virksomhederne hurtigere kan få den nødvendige arbejdskraft, samtidig med at de nytilkomne integreres ordentligt i det danske samfund.

Aftalen træder i kraft fra næste måned og forventes at berøre tusindvis af udenlandske statsborgere, der bor og arbejder i hele landet.`,
			url: "https://www.dr.dk/nyheder/politik/ny-aftale-udenlandsk-arbejdskraft",
			wantPaywall: false,
		},
		{
			name: "Valid full article from TV2 passes cleanly",
			content: `DSB meddeler fredag morgen, at en omfattende opgradering af signalsystemet på Sjælland vil medføre ændringer i køreplanerne i den kommende weekend.

Passagerer mellem København og Roskilde må forvente færre togafgange og forlænget rejsetid, da der indsættes togbusser på udvalgte strækninger.

Trafikchef hos DSB opfordrer alle rejsende til at tjekke Rejseplanen grundigt inden afgang for at undgå ubehagelige overraskelser.

Arbejdet forventes afsluttet søndag aften, så togtrafikken kan køre normalt igen fra mandag morgen.`,
			url: "https://nyheder.tv2.dk/samfund/dsb-aendrer-koereplaner",
			wantPaywall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPaywall, reason := IsPaywalledOrStub(tt.content, tt.url)
			if gotPaywall != tt.wantPaywall {
				t.Errorf("IsPaywalledOrStub() gotPaywall = %v, want %v (reason: %q)", gotPaywall, tt.wantPaywall, reason)
			}
			if tt.wantPaywall && tt.reasonSubstr != "" {
				if len(reason) == 0 {
					t.Errorf("expected reason to contain %q, but got empty reason", tt.reasonSubstr)
				}
			}
		})
	}
}
