package handlers

import "strings"

func redactEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return "[redacted]"
	}

	local := parts[0]
	domain := parts[1]
	if local == "" {
		return "***@" + domain
	}

	runes := []rune(local)
	return string(runes[0]) + "***@" + domain
}

func redactName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	runes := []rune(name)
	return string(runes[0]) + "***"
}
