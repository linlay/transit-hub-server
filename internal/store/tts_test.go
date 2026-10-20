package store

import (
	"math"
	"path/filepath"
	"testing"
)

func TestCharacterPricesRoundTripPrecisionAndValidation(t *testing.T) {
	db := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	defer db.Close()
	price := int64(5000)
	params := ModelPriceParams{Protocol: "minimax_tts", PublicModel: "speech", Billing: &PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}}
	stored, err := db.UpsertModelPrice(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.GetModelPrice(t.Context(), "minimax_tts", "speech")
	if err != nil || !ok || got.Billing.MicrocreditsPer10KCharacters == nil || *got.Billing.MicrocreditsPer10KCharacters != price {
		t.Fatalf("price=%+v err=%v", got, err)
	}
	if CharacterCost(1, stored) != 1 {
		t.Fatal("half-up rounding failed")
	}
	price = 4999
	if CharacterCost(1, stored) != 1 {
		t.Fatal("stored price changed")
	}
	stored.Billing.MicrocreditsPer10KCharacters = &price
	if CharacterCost(1, stored) != 0 {
		t.Fatal("sub-half rounding failed")
	}
	price = 9_000_000_000_000_000
	if CharacterCost(math.MaxInt64, stored) != math.MaxInt64 {
		t.Fatal("overflow did not saturate")
	}
	for _, mode := range []string{"tokens", "image", "free"} {
		if _, err := db.UpsertModelPrice(t.Context(), ModelPriceParams{Protocol: "minimax_tts", PublicModel: "bad", Billing: &PriceBilling{Mode: mode, MicrocreditsPer10KCharacters: &price}}); err == nil {
			t.Fatalf("accepted character price with %s mode", mode)
		}
	}
	params.Billing.MicrocreditsPer10KCharacters = nil
	if _, err := db.UpsertModelPrice(t.Context(), params); err == nil {
		t.Fatal("missing price accepted")
	}
	zero := int64(0)
	params.Billing.MicrocreditsPer10KCharacters = &zero
	if _, err := db.UpsertModelPrice(t.Context(), params); err == nil {
		t.Fatal("zero price must be explicit free")
	}
	price = 1000
	params.Billing.MicrocreditsPer10KCharacters = &price
	params.InputMicrocreditsPer1MTokens = 1
	if _, err := db.UpsertModelPrice(t.Context(), params); err == nil {
		t.Fatal("mixed token/character prices accepted")
	}
}

func TestTTSPriceProtocolMustMatchBillingMode(t *testing.T) {
	db := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	defer db.Close()
	price := int64(1000)
	if _, err := db.UpsertModelPrice(t.Context(), ModelPriceParams{Protocol: "openai", PublicModel: "bad", Billing: &PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}}); err == nil {
		t.Fatal("character price accepted for openai")
	}
	for _, protocol := range []string{"minimax_tts", "dashscope_tts"} {
		if _, err := db.UpsertModelPrice(t.Context(), ModelPriceParams{Protocol: protocol, PublicModel: "bad", InputMicrocreditsPer1MTokens: 1000}); err == nil {
			t.Fatal("token price accepted for tts")
		}
	}
}
