package service

import (
	"strings"
	"testing"
)

func TestNormalizePhoneNumber(t *testing.T) {
	t.Parallel()

	got, err := normalizePhoneNumber("7874387562")
	if err != nil {
		t.Fatalf("normalizePhoneNumber returned error: %v", err)
	}
	if got != "+17874387562" {
		t.Fatalf("expected +17874387562, got %s", got)
	}
}

func TestRequireUSOrPRNumberRejectsCanada(t *testing.T) {
	t.Parallel()

	err := requireUSOrPRNumber("+15145551234")
	if err == nil {
		t.Fatal("expected Canada number to be rejected")
	}
}

func TestPrepareSpeechTextDefaultsToTwoRepeats(t *testing.T) {
	t.Parallel()

	got, err := prepareSpeechText("market is open", 0)
	if err != nil {
		t.Fatalf("prepareSpeechText returned error: %v", err)
	}

	want := "market is open market is open"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeTopic(t *testing.T) {
	t.Parallel()

	got := sanitizeTopic(" rain alerts/now ")
	if got != "rainalertsnow" {
		t.Fatalf("unexpected topic: %q", got)
	}
}

func TestBuildObjectNameIncludesPrefix(t *testing.T) {
	t.Parallel()

	got := buildObjectName("communications-mcp")
	if !strings.HasPrefix(got, "communications-mcp/") {
		t.Fatalf("expected prefixed object name, got %q", got)
	}
	if !strings.HasSuffix(got, ".mp3") {
		t.Fatalf("expected .mp3 suffix, got %q", got)
	}
}
