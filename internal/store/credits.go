package store

import (
	"errors"
	"math"
	"math/big"
	"time"
)

// All persisted amounts are integer micro-Credits. Credits are the only billing unit.
const MicrocreditsPerCredit int64 = 1_000_000

func CostRemaining(quota, used int64) int64 {
	if quota == 0 {
		return 0
	}
	return quota - used
}

// Metadata retained independently of best-effort request logs.
func nonemptySnapshot(value string) string {
	if value == "" {
		return "null"
	}
	return value
}
func nullableStart(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

type ImagePrice struct {
	Size                string `json:"size"`
	Quality             string `json:"quality"`
	ChargedMicrocredits int64  `json:"charged_microcredits,string"`
}

// TokenPriceTier replaces the entire request tariff once input exceeds the threshold.
type TokenPriceTier struct {
	AboveInputTokens          int64  `json:"above_input_tokens"`
	InputMicrocreditsPer1M    int64  `json:"input_microcredits_per_1m_tokens,string"`
	OutputMicrocreditsPer1M   int64  `json:"output_microcredits_per_1m_tokens,string"`
	CacheHitMicrocreditsPer1M *int64 `json:"input_cache_hit_microcredits_per_1m_tokens,omitempty,string"`
}

type PriceBilling struct {
	TokenTiers                  []TokenPriceTier `json:"token_tiers,omitempty"`
	Mode                        string           `json:"mode"` // tokens, image, free
	CacheWriteMicrocreditsPer1M *int64           `json:"cache_write_microcredits_per_1m_tokens,omitempty,string"`
	ImagePrices                 []ImagePrice     `json:"image_prices,omitempty"`
}

func validateBilling(b PriceBilling, p ModelPriceParams) error {
	if b.Mode != "tokens" && b.Mode != "image" && b.Mode != "free" {
		return errors.New("billing.mode must be tokens, image or free")
	}
	for _, cost := range []int64{p.InputMicrocreditsPer1MTokens, p.OutputMicrocreditsPer1MTokens} {
		if cost < 0 || cost > 9_000_000_000_000_000 {
			return errors.New("price out of range")
		}
	}
	for _, cost := range []*int64{p.InputCacheHitMicrocreditsPer1MTokens, b.CacheWriteMicrocreditsPer1M} {
		if cost != nil && (*cost < 0 || *cost > 9_000_000_000_000_000) {
			return errors.New("price out of range")
		}
	}
	if b.Mode == "tokens" && p.InputMicrocreditsPer1MTokens == 0 && p.OutputMicrocreditsPer1MTokens == 0 && (p.InputCacheHitMicrocreditsPer1MTokens == nil || *p.InputCacheHitMicrocreditsPer1MTokens == 0) && (b.CacheWriteMicrocreditsPer1M == nil || *b.CacheWriteMicrocreditsPer1M == 0) {
		return errors.New("zero-priced models must explicitly use billing.mode=free")
	}
	if b.Mode == "image" && len(b.ImagePrices) == 0 {
		return errors.New("image billing requires image_prices")
	}
	previous := int64(-1)
	for _, tier := range b.TokenTiers {
		if b.Mode != "tokens" || tier.AboveInputTokens < 0 || tier.AboveInputTokens <= previous {
			return errors.New("token tiers require tokens mode and increasing non-negative thresholds")
		}
		params := ModelPriceParams{InputMicrocreditsPer1MTokens: tier.InputMicrocreditsPer1M, OutputMicrocreditsPer1MTokens: tier.OutputMicrocreditsPer1M, InputCacheHitMicrocreditsPer1MTokens: tier.CacheHitMicrocreditsPer1M}
		if err := validateBilling(PriceBilling{Mode: "tokens"}, params); err != nil {
			return err
		}
		previous = tier.AboveInputTokens
	}
	seen := map[string]bool{}
	for _, rule := range b.ImagePrices {
		key := rule.Size + "\x00" + rule.Quality
		if seen[key] || rule.ChargedMicrocredits <= 0 || rule.ChargedMicrocredits > 9_000_000_000_000_000 {
			return errors.New("invalid or duplicate image price")
		}
		seen[key] = true
	}
	return nil
}

func (p ModelPrice) ImageUnitCost(size, quality string) (int64, bool) {
	if p.Billing.Mode == "free" {
		return 0, true
	}
	// Empty selectors are wildcards. Prefer the most specific matching rule;
	// equal-specificity ambiguous matches are rejected rather than undercharged.
	best, value, matches := -1, int64(0), 0
	for _, rule := range p.Billing.ImagePrices {
		if rule.Size != "" && rule.Size != size || rule.Quality != "" && rule.Quality != quality {
			continue
		}
		score := 0
		if rule.Size != "" {
			score++
		}
		if rule.Quality != "" {
			score++
		}
		if score > best {
			best, value, matches = score, rule.ChargedMicrocredits, 1
		} else if score == best {
			matches++
		}
	}
	return value, matches == 1
}

// Sum at full precision, then round half up once to micro-Credits. Big integers
// prevent intermediate token*price overflow. Saturation can only deny more use.
func pricedSum(pairs ...int64) int64 {
	sum := new(big.Int)
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] > 0 && pairs[i+1] > 0 {
			sum.Add(sum, new(big.Int).Mul(big.NewInt(pairs[i]), big.NewInt(pairs[i+1])))
		}
	}
	sum.Add(sum, big.NewInt(500_000))
	sum.Quo(sum, big.NewInt(1_000_000))
	if !sum.IsInt64() {
		return math.MaxInt64
	}
	return sum.Int64()
}

func TokenCost(p ModelPrice, input, output, hit, write int64) int64 {
	if p.Billing.Mode == "free" {
		return 0
	}
	for _, tier := range p.Billing.TokenTiers {
		if input > tier.AboveInputTokens {
			p.InputMicrocreditsPer1MTokens = tier.InputMicrocreditsPer1M
			p.OutputMicrocreditsPer1MTokens = tier.OutputMicrocreditsPer1M
			p.InputCacheHitMicrocreditsPer1MTokens = tier.CacheHitMicrocreditsPer1M
		}
	}
	hit = max(0, min(hit, input))
	write = max(0, min(write, input-hit))
	hitPrice, writePrice := p.InputMicrocreditsPer1MTokens, p.InputMicrocreditsPer1MTokens
	if p.InputCacheHitMicrocreditsPer1MTokens != nil {
		hitPrice = *p.InputCacheHitMicrocreditsPer1MTokens
	}
	if p.Billing.CacheWriteMicrocreditsPer1M != nil {
		writePrice = *p.Billing.CacheWriteMicrocreditsPer1M
	}
	return pricedSum(max(0, input-hit-write), p.InputMicrocreditsPer1MTokens, hit, hitPrice, write, writePrice, output, p.OutputMicrocreditsPer1MTokens)
}

func ImageCost(count, unit int64) int64 {
	if count <= 0 || unit <= 0 {
		return 0
	}
	if count > math.MaxInt64/unit {
		return math.MaxInt64
	}
	return count * unit
}

func addCost(a, b int64) int64 {
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
