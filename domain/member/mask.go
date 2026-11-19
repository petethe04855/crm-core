package member

import "strings"

// MaskEmail hides the local part except its first two characters:
// "nopharat@gmail.com" -> "no******@gmail.com".
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return maskMiddle(email, 1, 0)
	}
	local, host := email[:at], email[at:]
	if len(local) == 1 {
		return "*" + host
	}
	keep := 2
	if len(local) <= keep {
		keep = 1
	}
	return local[:keep] + strings.Repeat("*", len(local)-keep) + host
}

// MaskPhone keeps the country prefix and the last three digits:
// "+66971166657" -> "+66******657".
func MaskPhone(phone string) string {
	if phone == "" {
		return ""
	}
	head := 0
	if strings.HasPrefix(phone, "+") && len(phone) > 3 {
		head = 3
	}
	return maskMiddle(phone, head, 3)
}

func maskMiddle(s string, head, tail int) string {
	if len(s) <= head+tail {
		return strings.Repeat("*", len(s))
	}
	return s[:head] + strings.Repeat("*", len(s)-head-tail) + s[len(s)-tail:]
}
