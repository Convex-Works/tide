package httpx

import (
	"strings"
	"unicode"
)

// Plain makes text a machine reported (its name, a progress message, an
// error) safe to show beside klisi's own: control characters become spaces,
// format characters such as bidirectional overrides and zero-width joiners
// are dropped so they can't reorder or hide what surrounds them, and runs of
// whitespace collapse to one space.
func Plain(text string) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cc, r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}
