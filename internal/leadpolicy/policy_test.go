package leadpolicy

import (
	"strings"
	"testing"
)

func TestPolicyUsesNativeModelSpecificAgents(t *testing.T) {
	document := Document()
	for _, want := range []string{"luna_executor", "gpt-5.6-sol", "astra_advisor", "gpt-6-astra", "two serious attempts", "Critical path"} {
		if !strings.Contains(document, want) {
			t.Fatalf("policy missing %q", want)
		}
	}
	reminder := RuntimeReminder("inspect the failure")
	if !IsPolicyPrompt(reminder) || !strings.Contains(reminder, "Operator request:\ninspect the failure") {
		t.Fatalf("unexpected reminder: %s", reminder)
	}
	upgrade := ApplyWithRequest("Axiom", "inspect the failure")
	if !strings.Contains(upgrade, "Lead Agent Policy") || !strings.Contains(upgrade, "## Operator request\n\ninspect the failure") {
		t.Fatalf("unexpected upgrade prompt: %s", upgrade)
	}
}

func TestCompatibilityNeverDowngradesNewerOrForeignPolicy(t *testing.T) {
	if CompatibilityFor("", 0) != NeedsApply || CompatibilityFor(ID, Version-1) != NeedsApply {
		t.Fatal("missing and older versions should be upgraded")
	}
	if CompatibilityFor(ID, Version) != Current {
		t.Fatal("current version should be current")
	}
	if CompatibilityFor(ID, Version+1) != Incompatible || CompatibilityFor("custom", 1) != Incompatible {
		t.Fatal("newer and foreign policies must not be downgraded")
	}
}
