package scraper

import (
	"fmt"
	"strings"
)

// paywallIndicators contains phrases commonly found in Danish paywalled articles or subscriber blocks.
var paywallIndicators = []string{
	"kræver abonnement",
	"forbeholdt abonnenter",
	"kun for abonnenter",
	"bliv abonnent",
	"køb abonnement",
	"tegne abonnement",
	"opret abonnement",
	"låst artikel",
	"låst indhold",
	"eksklusivt for abonnenter",
	"denne artikel er kun tilgængelig for abonnenter",
	"prøv 1 måned",
	"prøv en måned",
	"få adgang til hele artiklen",
	"få fuld adgang",
	"køb adgang",
	"få ubegrænset adgang",
	"log ind for at læse videre",
	"log ind for at læse hele",
	"artiklen kan kun læses af abonnenter",
	"dette er en plus-artikel",
	"plus-artikel",
	"er du allerede abonnent",
	"abonnér nu",
	"bestil abonnement",
	"køb digital adgang",
	"adgang til artiklen",
}

// knownPaywallDomains are publishers where many articles have hard/dynamic paywalls.
var knownPaywallDomains = []string{
	"berlingske.dk",
	"politiken.dk",
	"jyllands-posten.dk",
	"jp.dk",
	"borsen.dk",
	"information.dk",
	"weekendavisen.dk",
	"kristeligt-dagblad.dk",
	"finans.dk",
}

// IsPaywalledOrStub checks if an extracted article content is paywalled, a stub, or an empty teaser.
// It returns true and a human-readable reason if the article should be discarded before sending to AI.
func IsPaywalledOrStub(content string, articleURL string) (bool, string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true, "empty content"
	}

	lowerContent := strings.ToLower(trimmed)
	lowerURL := strings.ToLower(articleURL)

	// 1. Explicit paywall indicator phrases
	for _, phrase := range paywallIndicators {
		if strings.Contains(lowerContent, phrase) {
			return true, fmt.Sprintf("contains paywall phrase: %q", phrase)
		}
	}

	// 2. Count runes and paragraphs
	runes := []rune(trimmed)
	runeCount := len(runes)
	paragraphs := strings.Split(trimmed, "\n\n")

	// Filter out tiny 1-line fragments from paragraph count
	realParagraphCount := 0
	for _, p := range paragraphs {
		if len(strings.TrimSpace(p)) > 40 {
			realParagraphCount++
		}
	}

	// 3. Absolute minimum threshold: any article with less than 250 characters is a stub
	if runeCount < 250 {
		return true, fmt.Sprintf("stub content too short (%d chars, min 250)", runeCount)
	}

	// 4. Known paywall publisher check: paywalls on Berlingske/Politiken/JP often leak 1 teaser paragraph
	for _, domain := range knownPaywallDomains {
		if strings.Contains(lowerURL, domain) {
			if runeCount < 400 || realParagraphCount < 2 {
				return true, fmt.Sprintf("paywalled publisher %s with teaser content (%d chars, %d paragraphs)", domain, runeCount, realParagraphCount)
			}
		}
	}

	return false, ""
}

