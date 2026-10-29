package common

import "strings"

const DefaultSystemName = "aiscoo"
const DefaultLogo = "/brand/app-light.svg"

// NormalizeSystemName upgrades legacy product defaults without replacing custom site names.
func NormalizeSystemName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "new api", "newapi", "new-api", "one api", "oneapi", "one-api", "xwork", "xcode", "aiscoo":
		return DefaultSystemName
	default:
		return name
	}
}

func NormalizeSystemLogo(logo string) string {
	switch strings.TrimSpace(logo) {
	case "", "/logo.png", "/favicon.ico":
		return DefaultLogo
	default:
		return logo
	}
}
