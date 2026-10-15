package templates

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// funcs returns the helpers available in every template. They are
// deliberately limited to formatting: templates must not be able to emit
// unescaped HTML from user data.
func funcs() map[string]any {
	return map[string]any{
		"default": defaultValue,
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"trim":    strings.TrimSpace,
		"year":    func() int { return time.Now().Year() },
		"date":    formatDate,
	}
}

// defaultValue returns def when v is empty: {{ .name | default "there" }}.
func defaultValue(def, v any) any {
	if v == nil {
		return def
	}
	rv := reflect.ValueOf(v)
	if rv.IsZero() {
		return def
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.String:
		if rv.Len() == 0 {
			return def
		}
	}
	return v
}

// formatDate formats an RFC 3339 string or a time.Time with a Go layout:
// {{ date "02/01/2006" .created_at }}.
func formatDate(layout string, v any) (string, error) {
	switch t := v.(type) {
	case time.Time:
		return t.Format(layout), nil
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return "", fmt.Errorf("date: %w", err)
		}
		return parsed.Format(layout), nil
	default:
		return "", fmt.Errorf("date: unsupported type %T", v)
	}
}
