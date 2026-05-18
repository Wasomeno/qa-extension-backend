package services

import (
	"strings"
	"testing"
)

func TestRelevantProjectTestContextPrefersMatchingAndUsefulSections(t *testing.T) {
	markdown := strings.TrimSpace(`# Project Test Context

## Test Users
- Admin user: admin@example.com
- Regular user: user@example.com

## Payment Fixtures
- Paid customer: paid@example.com

## Payroll Rules
- Admin users can approve overtime requests.

## E2E Selectors
- Submit button uses [data-testid="submit"]
`)

	got := RelevantProjectTestContext(markdown, "admin approves overtime request", 500)

	for _, want := range []string{"## Test Users", "## Payroll Rules", "Admin users can approve overtime"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected context to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRelevantProjectTestContextTruncatesLargeContext(t *testing.T) {
	markdown := "## Test Users\n" + strings.Repeat("admin user details\n", 100)
	got := RelevantProjectTestContext(markdown, "admin user", 250)

	if len(got) > 250 {
		t.Fatalf("expected context to be truncated to max bytes, got length %d", len(got))
	}
	if !strings.Contains(got, "Project test context truncated") {
		t.Fatalf("expected truncation marker, got:\n%s", got)
	}
}
