package store

import (
	"errors"
	"math"
	"math/big"
	"time"
)

// All persisted amounts are integer micro-CNY; Credits are a presentation unit.
const MicroPerCredit int64 = 10_000

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
	Size      string `json:"size"`
	Quality   string `json:"quality"`
	CostMicro int64  `json:"cost_micro"`
}

type PriceBilling struct {
	Mode                     string       `json:"mode"` // tokens, image, free
	CacheWriteCostMicroPer1M *int64       `json:"cache_write_cost_micro_per_1m_tokens,omitempty"`
	ImagePrices              []ImagePrice `json:"image_prices,omitempty"`
}

func validateBilling(b PriceBilling, p ModelPriceParams) error {
	if b.Mode != "tokens" && b.Mode != "image" && b.Mode != "free" {
		return errors.New("billing.mode must be tokens, image or free")
	}
	for _, cost := range []int64{p.InputCostMicroPer1MTokens, p.OutputCostMicroPer1MTokens} {
		if cost < 0 || cost > 9_000_000_000_000_000 {
			return errors.New("price out of range")
		}
	}
	for _, cost := range []*int64{p.InputCacheHitCostMicroPer1MTokens, b.CacheWriteCostMicroPer1M} {
		if cost != nil && (*cost < 0 || *cost > 9_000_000_000_000_000) {
			return errors.New("price out of range")
		}
	}
	if b.Mode == "tokens" && p.InputCostMicroPer1MTokens == 0 && p.OutputCostMicroPer1MTokens == 0 && (p.InputCacheHitCostMicroPer1MTokens == nil || *p.InputCacheHitCostMicroPer1MTokens == 0) && (b.CacheWriteCostMicroPer1M == nil || *b.CacheWriteCostMicroPer1M == 0) {
		return errors.New("zero-priced models must explicitly use billing.mode=free")
	}
	if b.Mode == "image" && len(b.ImagePrices) == 0 {
		return errors.New("image billing requires image_prices")
	}
	seen := map[string]bool{}
	for _, rule := range b.ImagePrices {
		key := rule.Size + "\x00" + rule.Quality
		if seen[key] || rule.CostMicro <= 0 || rule.CostMicro > 9_000_000_000_000_000 {
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
			best, value, matches = score, rule.CostMicro, 1
		} else if score == best {
			matches++
		}
	}
	return value, matches == 1
}

// Sum at full precision, then round half up once to micro-CNY. Big integers
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
	hit = max(0, min(hit, input))
	write = max(0, min(write, input-hit))
	hitPrice, writePrice := p.InputCostMicroPer1MTokens, p.InputCostMicroPer1MTokens
	if p.InputCacheHitCostMicroPer1MTokens != nil {
		hitPrice = *p.InputCacheHitCostMicroPer1MTokens
	}
	if p.Billing.CacheWriteCostMicroPer1M != nil {
		writePrice = *p.Billing.CacheWriteCostMicroPer1M
	}
	return pricedSum(max(0, input-hit-write), p.InputCostMicroPer1MTokens, hit, hitPrice, write, writePrice, output, p.OutputCostMicroPer1MTokens)
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
