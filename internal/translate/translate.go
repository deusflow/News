// Package translate deprecated: replaced by Gemini summarization. DO NOT USE.
package translate

import "errors"

// TranslateText always returns an error (legacy stub).
func TranslateText(text, from, to string) (string, error) {
	return "", errors.New("translate package deprecated; use gemini client")
}
