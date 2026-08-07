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

type Entry struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

const (
	maxRenderedEntries    = 100
	maxRenderedTitleBytes = 512
	maxRenderedLineBytes  = 4096
	maxRenderedEntryBytes = 64 << 10
)

var errOmitTemplateLine = errors.New("omit template line")

func parseEntries(body []byte, cfg config.ProviderQuotaConfig) ([]Entry, error) {
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
	if len(items) > maxRenderedEntries {
		return nil, fmt.Errorf("configured quota items exceed limit of %d", maxRenderedEntries)
	}

	entries := make([]Entry, 0, len(items))
	for index, item := range items {
		entry, err := renderEntry(root, item, cfg)
		if err != nil {
			return nil, fmt.Errorf("quota item %d: %w", index, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func renderEntry(root, item any, cfg config.ProviderQuotaConfig) (Entry, error) {
	titleTemplate, err := config.ParseProviderQuotaTemplate(cfg.Display.Title)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid title template: %w", err)
	}
	title, complete, err := renderTemplate(titleTemplate, root, item, cfg)
	if err != nil {
		return Entry{}, fmt.Errorf("render title: %w", err)
	}
	title = strings.TrimSpace(title)
	if !complete || title == "" {
		return Entry{}, errors.New("title fields were not found")
	}
	if len(title) > maxRenderedTitleBytes {
		return Entry{}, errors.New("rendered title is too large")
	}

	lines := make([]string, 0, len(cfg.Display.Lines))
	renderedBytes := len(title)
	for index, rawLine := range cfg.Display.Lines {
		lineTemplate, err := config.ParseProviderQuotaTemplate(rawLine)
		if err != nil {
			return Entry{}, fmt.Errorf("invalid line template %d: %w", index, err)
		}
		line, complete, err := renderTemplate(lineTemplate, root, item, cfg)
		if err != nil {
			return Entry{}, fmt.Errorf("render line %d: %w", index, err)
		}
		line = strings.TrimSpace(line)
		if !complete || line == "" {
			continue
		}
		if len(line) > maxRenderedLineBytes {
			return Entry{}, fmt.Errorf("rendered line %d is too large", index)
		}
		renderedBytes += len(line)
		if renderedBytes > maxRenderedEntryBytes {
			return Entry{}, errors.New("rendered entry is too large")
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return Entry{}, errors.New("no display lines could be rendered")
	}
	return Entry{Title: title, Lines: lines}, nil
}

func renderTemplate(template config.ProviderQuotaTemplate, root, item any, cfg config.ProviderQuotaConfig) (string, bool, error) {
	var output strings.Builder
	for _, part := range template.Parts {
		if part.Field == "" {
			output.WriteString(part.Text)
			continue
		}
		value, ok := mappedValue(root, item, cfg.Fields[part.Field])
		if !ok || value == nil {
			return "", false, nil
		}
		var err error
		for _, formatter := range part.Formatters {
			value, err = applyFormatter(value, formatter, cfg.Display.ValueMaps)
			if err != nil {
				if errors.Is(err, errOmitTemplateLine) {
					return "", false, nil
				}
				return "", false, fmt.Errorf("field %q: %w", part.Field, err)
			}
		}
		display, err := scalarDisplayString(value)
		if err != nil {
			return "", false, fmt.Errorf("field %q must resolve to a scalar", part.Field)
		}
		output.WriteString(display)
	}
	return output.String(), true, nil
}

func applyFormatter(value any, formatter config.ProviderQuotaTemplateFormatter, valueMaps map[string]map[string]string) (any, error) {
	switch formatter.Name {
	case "scale":
		number, err := numericValue(value)
		if err != nil {
			return nil, errors.New("scale requires a numeric value")
		}
		scale, _ := strconv.ParseFloat(formatter.Argument, 64)
		return number * scale, nil
	case "number", "percent":
		number, err := numericValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s requires a numeric value", formatter.Name)
		}
		precision := -1
		if formatter.Argument != "" {
			precision, _ = strconv.Atoi(formatter.Argument)
		}
		formatted := formatNumber(number, precision)
		if formatter.Name == "percent" {
			formatted += "%"
		}
		return formatted, nil
	case "datetime":
		parsed, err := timeValue(value)
		if err != nil {
			return nil, errors.New("datetime requires a Unix timestamp or RFC3339 value")
		}
		location := time.UTC
		if formatter.Argument != "" {
			location, _ = time.LoadLocation(formatter.Argument)
		}
		return parsed.In(location).Format("2006-01-02 15:04:05 -07:00"), nil
	case "duration":
		seconds, err := numericValue(value)
		if err != nil {
			return nil, errors.New("duration requires numeric seconds")
		}
		return formatDuration(seconds, formatter.Argument), nil
	case "map":
		key, err := scalarMapKey(value)
		if err != nil {
			return nil, errors.New("map requires a scalar value")
		}
		if mapped, ok := valueMaps[formatter.Argument][key]; ok {
			return mapped, nil
		}
		return value, nil
	case "nonzero":
		number, err := numericValue(value)
		if err != nil {
			return nil, errors.New("nonzero requires a numeric value")
		}
		if number == 0 {
			return nil, errOmitTemplateLine
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported formatter %q", formatter.Name)
	}
}

func mappedValue(root, item any, configuredPath string) (any, bool) {
	path := strings.TrimSpace(configuredPath)
	base := item
	switch {
	case strings.HasPrefix(path, "$root."):
		base = root
		path = strings.TrimPrefix(path, "$root.")
	case strings.HasPrefix(path, "$item."):
		path = strings.TrimPrefix(path, "$item.")
	}
	return lookupPath(base, path)
}

func lookupPath(root any, path string) (any, bool) {
	current := root
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, false
		}
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[segment]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
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

func scalarDisplayString(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case float64:
		return formatNumber(value, -1), nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		return value.String(), nil
	default:
		return "", errors.New("not a scalar")
	}
}

func scalarMapKey(value any) (string, error) {
	switch value := value.(type) {
	case float64:
		return formatNumber(value, -1), nil
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		return value.String(), nil
	default:
		return "", errors.New("not a scalar")
	}
}

func formatNumber(value float64, precision int) string {
	if precision < 0 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	formatted := strconv.FormatFloat(value, 'f', precision, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	if formatted == "-0" {
		return "0"
	}
	return formatted
}

func formatDuration(rawSeconds float64, locale string) string {
	seconds := int64(math.Round(math.Abs(rawSeconds)))
	negative := rawSeconds < 0
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60

	zh := strings.EqualFold(locale, "zh") || strings.EqualFold(locale, "zh-CN")
	parts := make([]string, 0, 4)
	appendPart := func(value int64, enUnit, zhUnit string) {
		if value == 0 {
			return
		}
		if zh {
			parts = append(parts, fmt.Sprintf("%d %s", value, zhUnit))
			return
		}
		parts = append(parts, fmt.Sprintf("%d%s", value, enUnit))
	}
	appendPart(days, "d", "天")
	appendPart(hours, "h", "小时")
	appendPart(minutes, "m", "分钟")
	appendPart(seconds, "s", "秒")
	if len(parts) == 0 {
		if zh {
			parts = append(parts, "0 秒")
		} else {
			parts = append(parts, "0s")
		}
	}
	formatted := strings.Join(parts, " ")
	if negative {
		return "-" + formatted
	}
	return formatted
}
