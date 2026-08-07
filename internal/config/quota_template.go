package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type ProviderQuotaTemplate struct {
	Parts []ProviderQuotaTemplatePart
}

type ProviderQuotaTemplatePart struct {
	Text       string
	Field      string
	Formatters []ProviderQuotaTemplateFormatter
}

type ProviderQuotaTemplateFormatter struct {
	Name     string
	Argument string
}

func ParseProviderQuotaTemplate(input string) (ProviderQuotaTemplate, error) {
	parts := make([]ProviderQuotaTemplatePart, 0, 4)
	rest := input
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			if strings.Contains(rest, "}}") {
				return ProviderQuotaTemplate{}, errors.New("unexpected closing braces")
			}
			if rest != "" {
				parts = append(parts, ProviderQuotaTemplatePart{Text: rest})
			}
			break
		}
		if strings.Contains(rest[:start], "}}") {
			return ProviderQuotaTemplate{}, errors.New("unexpected closing braces")
		}
		if start > 0 {
			parts = append(parts, ProviderQuotaTemplatePart{Text: rest[:start]})
		}
		tail := rest[start+2:]
		end := strings.Index(tail, "}}")
		if end < 0 {
			return ProviderQuotaTemplate{}, errors.New("unclosed placeholder")
		}
		expression := strings.TrimSpace(tail[:end])
		if expression == "" || strings.Contains(expression, "{{") {
			return ProviderQuotaTemplate{}, errors.New("invalid placeholder")
		}
		part, err := parseProviderQuotaPlaceholder(expression)
		if err != nil {
			return ProviderQuotaTemplate{}, err
		}
		parts = append(parts, part)
		rest = tail[end+2:]
	}
	return ProviderQuotaTemplate{Parts: parts}, nil
}

func parseProviderQuotaPlaceholder(expression string) (ProviderQuotaTemplatePart, error) {
	segments := strings.Split(expression, "|")
	field := strings.TrimSpace(segments[0])
	if !validQuotaTemplateIdentifier(field) {
		return ProviderQuotaTemplatePart{}, fmt.Errorf("invalid field identifier %q", field)
	}
	part := ProviderQuotaTemplatePart{Field: field}
	for _, segment := range segments[1:] {
		raw := strings.TrimSpace(segment)
		if raw == "" {
			return ProviderQuotaTemplatePart{}, fmt.Errorf("field %q has an empty formatter", field)
		}
		name, argument, _ := strings.Cut(raw, ":")
		formatter := ProviderQuotaTemplateFormatter{
			Name:     strings.ToLower(strings.TrimSpace(name)),
			Argument: strings.TrimSpace(argument),
		}
		if err := validateProviderQuotaFormatter(formatter); err != nil {
			return ProviderQuotaTemplatePart{}, fmt.Errorf("field %q: %w", field, err)
		}
		part.Formatters = append(part.Formatters, formatter)
	}
	return part, nil
}

func validateProviderQuotaFormatter(formatter ProviderQuotaTemplateFormatter) error {
	switch formatter.Name {
	case "scale":
		value, err := strconv.ParseFloat(formatter.Argument, 64)
		if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("scale requires a positive finite number")
		}
	case "number", "percent":
		if formatter.Argument == "" {
			return nil
		}
		precision, err := strconv.Atoi(formatter.Argument)
		if err != nil || precision < 0 || precision > 6 {
			return fmt.Errorf("%s precision must be between 0 and 6", formatter.Name)
		}
	case "datetime":
		if formatter.Argument != "" {
			if _, err := time.LoadLocation(formatter.Argument); err != nil {
				return fmt.Errorf("datetime timezone %q is invalid", formatter.Argument)
			}
		}
	case "duration":
		switch strings.ToLower(formatter.Argument) {
		case "", "en", "zh", "zh-cn":
		default:
			return errors.New("duration locale must be en or zh-CN")
		}
	case "map":
		if !validQuotaTemplateIdentifier(formatter.Argument) {
			return errors.New("map requires a valid value map name")
		}
	case "nonzero":
		if formatter.Argument != "" {
			return errors.New("nonzero does not accept an argument")
		}
	default:
		return fmt.Errorf("unsupported formatter %q", formatter.Name)
	}
	return nil
}

func validQuotaTemplateIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validQuotaFieldPath(value string) bool {
	path := strings.TrimSpace(value)
	if path == "" {
		return false
	}
	for _, prefix := range []string{"$root.", "$item."} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	if path == "" || strings.HasPrefix(path, "$") {
		return false
	}
	for _, segment := range strings.Split(path, ".") {
		if segment == "" || segment != strings.TrimSpace(segment) {
			return false
		}
	}
	return true
}
