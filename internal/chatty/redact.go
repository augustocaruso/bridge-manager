package chatty

import "strings"

func sensitive(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "key") || key == "uri" || key == "username" || key == "sender_localpart"
}
func Redact(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, child := range typed {
			if sensitive(key) {
				result[key] = "[REDACTED]"
			} else if key == "permissions" {
				result[key] = map[string]any{"[REDACTED]": "admin"}
			} else {
				result[key] = Redact(child)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = Redact(child)
		}
		return result
	default:
		return value
	}
}
