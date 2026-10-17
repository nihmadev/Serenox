package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProfileActionRoundTripAndBounds(t *testing.T) {
	action := Action{ID: "profile-1", Kind: ActionProfileUpdateName, IdempotencyKey: "minute-1", Profile: &ProfileNameAction{Operation: ProfileUpdate, FirstName: "Alice | Bob | 14:37", LastName: "Smith", Generation: 2, LeaseToken: "opaque"}}
	if err := action.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Action
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Profile == nil || decoded.Profile.FirstName != action.Profile.FirstName {
		t.Fatalf("profile round trip: %#v", decoded.Profile)
	}
	for _, invalid := range []Action{
		{ID: "a", Kind: ActionProfileUpdateName, Profile: &ProfileNameAction{Operation: ProfileAcquire}},
		{ID: "a", Kind: ActionProfileUpdateName, IdempotencyKey: "k", Profile: &ProfileNameAction{Operation: ProfileUpdate, FirstName: "", Generation: 1, LeaseToken: "x"}},
		{ID: "a", Kind: ActionProfileUpdateName, IdempotencyKey: "k", Profile: &ProfileNameAction{Operation: ProfileUpdate, FirstName: strings.Repeat("x", 65), Generation: 1, LeaseToken: "x"}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid profile action accepted: %#v", invalid)
		}
	}
}
