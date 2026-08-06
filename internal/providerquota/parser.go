package providerquota

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

type Window struct {
	StartAt          *time.Time `json:"start_at,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	RemainingSeconds *float64   `json:"remaining_seconds,omitempty"`
	TotalCount       *float64   `json:"total_count,omitempty"`
	UsedCount        *float64   `json:"used_count,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	Status           string     `json:"status"`
	RawStatus        any        `json:"raw_status,omitempty"`
}

type Quota struct {
	ModelName             string   `json:"model_name"`
	Current               Window   `json:"current"`
	Weekly                Window   `json:"weekly"`
	WeeklyBoostMultiplier *float64 `json:"weekly_boost_multiplier,omitempty"`
}

func parseQuotas(body []byte, cfg config.ProviderQuotaConfig) ([]Quota, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("invalid JSON response")
	}

	itemsValue := root
	if path := strings.TrimSpace(cfg.ItemsPath); path != "" {
		var ok bool
		itemsValue, ok = lookupPath(root, path)
		if !ok {
			return nil, fmt.Errorf("items_path %q was not found", path)
		}
	}

	var items []any
	switch value := itemsValue.(type) {
	case []any:
		items = value
	case map[string]any:
		items = []any{value}
	default:
		return nil, errors.New("configured quota items must be an array or object")
	}

	quotas := make([]Quota, 0, len(items))
	for index, item := range items {
		quota, err := parseQuotaItem(item, cfg)
		if err != nil {
			return nil, fmt.Errorf("quota item %d: %w", index, err)
		}
		quotas = append(quotas, quota)
	}
	return quotas, nil
}

func parseQuotaItem(item any, cfg config.ProviderQuotaConfig) (Quota, error) {
	nameValue, ok := mappedValue(item, cfg, "model_name")
	if !ok {
		return Quota{}, errors.New("model_name was not found")
	}
	name, err := stringValue(nameValue)
	if err != nil || strings.TrimSpace(name) == "" {
		return Quota{}, errors.New("model_name must be a non-empty scalar")
	}

	quota := Quota{
		ModelName: strings.TrimSpace(name),
		Current:   Window{Status: "unknown"},
		Weekly:    Window{Status: "unknown"},
	}
	if err := populateWindow(item, cfg, "current", &quota.Current); err != nil {
		return Quota{}, err
	}
	if err := populateWindow(item, cfg, "weekly", &quota.Weekly); err != nil {
		return Quota{}, err
	}
	if value, ok := mappedValue(item, cfg, "weekly_boost_multiplier"); ok {
		parsed, err := numericValue(value)
		if err != nil {
			return Quota{}, errors.New("weekly_boost_multiplier must be numeric")
		}
		parsed *= scaleFor(cfg, "weekly_boost_multiplier")
		quota.WeeklyBoostMultiplier = &parsed
	}
	return quota, nil
}

func populateWindow(item any, cfg config.ProviderQuotaConfig, prefix string, window *Window) error {
	timeFields := []struct {
		name string
		dest **time.Time
	}{
		{prefix + "_start_time", &window.StartAt},
		{prefix + "_end_time", &window.ResetAt},
	}
	for _, field := range timeFields {
		if value, ok := mappedValue(item, cfg, field.name); ok {
			parsed, err := timeValue(value)
			if err != nil {
				return fmt.Errorf("%s must be a Unix timestamp or RFC3339 time", field.name)
			}
			*field.dest = &parsed
		}
	}

	numericFields := []struct {
		name string
		dest **float64
	}{
		{prefix + "_remaining_seconds", &window.RemainingSeconds},
		{prefix + "_total_count", &window.TotalCount},
		{prefix + "_used_count", &window.UsedCount},
		{prefix + "_remaining_percent", &window.RemainingPercent},
	}
	for _, field := range numericFields {
		if value, ok := mappedValue(item, cfg, field.name); ok {
			parsed, err := numericValue(value)
			if err != nil {
				return fmt.Errorf("%s must be numeric", field.name)
			}
			parsed *= scaleFor(cfg, field.name)
			*field.dest = &parsed
		}
	}

	if value, ok := mappedValue(item, cfg, prefix+"_status"); ok {
		raw, err := scalarValue(value)
		if err != nil {
			return fmt.Errorf("%s_status must be a scalar", prefix)
		}
		window.RawStatus = raw
		window.Status = normalizeStatus(raw, cfg.StatusValues)
	}
	return nil
}

func mappedValue(item any, cfg config.ProviderQuotaConfig, field string) (any, bool) {
	path := strings.TrimSpace(cfg.Fields[field])
	if path == "" {
		return nil, false
	}
	return lookupPath(item, path)
}

func lookupPath(root any, path string) (any, bool) {
	current := root
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func scaleFor(cfg config.ProviderQuotaConfig, field string) float64 {
	if scale := cfg.Scales[field]; scale > 0 {
		return scale
	}
	return 1
}

func numericValue(value any) (float64, error) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, err
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, err
		}
		number = parsed
	default:
		return 0, errors.New("not numeric")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, errors.New("not finite")
	}
	return number, nil
}

func timeValue(value any) (time.Time, error) {
	if raw, ok := value.(string); ok {
		raw = strings.TrimSpace(raw)
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	number, err := numericValue(value)
	if err != nil {
		return time.Time{}, err
	}
	seconds := number
	if math.Abs(number) >= 1e12 {
		seconds = number / 1000
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), nil
}

func stringValue(value any) (string, error) {
	scalar, err := scalarValue(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(scalar), nil
}

func scalarValue(value any) (any, error) {
	switch value := value.(type) {
	case string, float64, bool, nil:
		return value, nil
	case json.Number:
		return value.String(), nil
	default:
		return nil, errors.New("not a scalar")
	}
}

func normalizeStatus(raw any, values map[string]string) string {
	key := fmt.Sprint(raw)
	if number, ok := raw.(float64); ok && number == math.Trunc(number) {
		key = strconv.FormatInt(int64(number), 10)
	}
	normalized := strings.ToLower(strings.TrimSpace(values[key]))
	switch normalized {
	case "normal", "exhausted", "unlimited", "unknown":
		return normalized
	default:
		return "unknown"
	}
}
