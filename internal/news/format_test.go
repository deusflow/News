package news

import "testing"

func TestRemoveTitleFromSummary_UnicodeSafeTrim(t *testing.T) {
	title := "Maersk stopper alle sejladser"
	summary := "Maersk stopper alle sejladser. Ruten lukkes midlertidigt af sikkerhedshensyn."

	got := removeTitleFromSummary(summary, title)
	want := "Ruten lukkes midlertidigt af sikkerhedshensyn."
	if got != want {
		t.Fatalf("unexpected trimmed summary\nwant: %q\n got: %q", want, got)
	}
}

func TestRemoveTitleFromSummary_DoesNotTrimWhenNotPrefix(t *testing.T) {
	title := "IKEA kommer til Randers"
	summary := "Projektet bliver storre end forst antaget."

	got := removeTitleFromSummary(summary, title)
	if got != summary {
		t.Fatalf("expected summary to remain unchanged, got: %q", got)
	}
}
