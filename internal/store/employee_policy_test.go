package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmployeeDefaultsPersistAndOnlyApplyToMissingIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := OpenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	fallback := CreateAPIKeyParams{AllowedModels: []string{"original"}, RequestQuota: 5}
	identity := EmployeeIdentity{Issuer: "issuer", Claim: "staffuserid", Subject: "existing"}
	existing, err := db.ResolveEmployeeKey(t.Context(), identity, fallback)
	if err != nil {
		t.Fatal(err)
	}
	defaults := EmployeePolicy{AllowedModels: []string{"updated"}, RequestQuota: 17, TokenQuota: 300,
		QuotaMicrocredits: 9007199254740993, RateLimits: []RateLimit{{Window: "1h", RequestQuota: 3, QuotaMicrocredits: 1234567}}}
	if err = db.SetEmployeePolicy(t.Context(), defaults); err != nil {
		t.Fatal(err)
	}
	disabled := "disabled"
	if _, err = db.UpdateAPIKey(t.Context(), existing.ID, APIKeyPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.GetEmployeePolicy(t.Context(), EmployeePolicy{})
	if err != nil || !reflect.DeepEqual(saved, defaults) {
		t.Fatalf("defaults not persisted exactly: %+v %v", saved, err)
	}
	same, err := db.ResolveEmployeeKey(t.Context(), identity, fallback)
	if err != nil || same.ID != existing.ID || same.RequestQuota != 5 || same.Status != "disabled" || !reflect.DeepEqual(same.AllowedModels, []string{"original"}) {
		t.Fatalf("existing employee changed: %+v %v", same, err)
	}
	identity.Subject = "missing"
	created, err := db.ResolveEmployeeKey(t.Context(), identity, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if created.RequestQuota != defaults.RequestQuota || created.TokenQuota != defaults.TokenQuota || created.QuotaMicrocredits != defaults.QuotaMicrocredits || !reflect.DeepEqual(created.AllowedModels, defaults.AllowedModels) || !reflect.DeepEqual(created.RateLimits, defaults.RateLimits) || created.KeyPrefix != "" {
		t.Fatalf("employee did not inherit defaults: %+v", created)
	}
}
