package gemini

import (
	"errors"
	"testing"
)

func TestIsTransient503OrOverload(t *testing.T) {
	transient := []string{
		"Error 503, Message: This model is currently experiencing high demand., Status: UNAVAILABLE",
		"Error 504, Message: Deadline expired before operation could complete., Status: DEADLINE_EXCEEDED",
		"context deadline exceeded",
	}
	for _, msg := range transient {
		if !isTransient503OrOverload(errors.New(msg)) {
			t.Errorf("expected transient match for %q", msg)
		}
	}

	if isTransient503OrOverload(errors.New("Error 400, Message: Invalid API key")) {
		t.Errorf("expected 400 invalid API key not to be treated as transient overload")
	}
}

func TestIsThinkingConfigUnsupported(t *testing.T) {
	if !isThinkingConfigUnsupported(errors.New("Error 400, Message: thinkingLevel is not supported by this model, Status: INVALID_ARGUMENT")) {
		t.Fatal("expected unsupported thinkingLevel 400 error to be detected")
	}
	if isThinkingConfigUnsupported(errors.New("Error 503, Message: UNAVAILABLE")) {
		t.Fatal("expected 503 not to match thinking config unsupported")
	}
}
