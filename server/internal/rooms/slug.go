package rooms

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	minSlugLength = 3
	maxSlugLength = 64
)

var customSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// slugLetters is the alphabet of default slugs.
const slugLetters = "abcdefghijklmnopqrstuvwxyz"

// slugByteLimit is the largest multiple of 26 a byte can hold. A byte below
// it, taken mod 26, gives each letter equally often; a byte at or above it
// would favor the first letters, so it is drawn again.
const slugByteLimit = 256 / len(slugLetters) * len(slugLetters) // 234

// slugGroups are the lengths of a default slug's hyphenated groups: ten
// letters, abc-defg-hij, log2(26^10) ≈ 47 bits.
var slugGroups = [...]int{3, 4, 3}

// slugLength is how many letters a default slug has.
const slugLength = 10

// GenerateSlug returns a default room slug: ten letters a–z drawn uniformly
// from crypto/rand, grouped 3-4-3 (abc-defg-hij), short enough to read
// aloud and, behind the join rate limit, too many to guess at
// (ARCHITECTURE.md §5). Owners can replace it with a slug of their own.
func GenerateSlug() (string, error) {
	return generateSlug(rand.Reader)
}

// generateSlug draws a default slug from random, by rejection sampling:
// each letter is a byte below slugByteLimit, mod 26. It reads only as many
// bytes as it still needs letters, so it never reads more than it uses.
func generateSlug(random io.Reader) (string, error) {
	var buffer [slugLength]byte
	letters := make([]byte, 0, slugLength)
	for len(letters) < slugLength {
		draw := buffer[:slugLength-len(letters)]
		if _, err := io.ReadFull(random, draw); err != nil {
			return "", fmt.Errorf("draw a room slug: %w", err)
		}
		for _, b := range draw {
			if int(b) < slugByteLimit {
				letters = append(letters, slugLetters[int(b)%len(slugLetters)])
			}
		}
	}
	var slug strings.Builder
	for group, length := range slugGroups {
		if group > 0 {
			slug.WriteByte('-')
		}
		slug.Write(letters[:length])
		letters = letters[length:]
	}
	return slug.String(), nil
}

func normalizeSlug(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateSlug(value string) error {
	if len(value) < minSlugLength || len(value) > maxSlugLength {
		return fmt.Errorf("room slug must be between %d and %d characters", minSlugLength, maxSlugLength)
	}
	if !customSlugPattern.MatchString(value) {
		return fmt.Errorf("room slug may contain lowercase letters, numbers, and single hyphens")
	}
	return nil
}
