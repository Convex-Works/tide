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

// GenerateSlug returns a standards-shaped random UUID v4. Default room links
// are intentionally opaque; owners can replace them with a memorable custom
// slug from room settings.
func GenerateSlug() (string, error) {
	return generateSlug(rand.Reader)
}

func generateSlug(reader io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", err
	}
	// RFC 9562 UUID version and variant bits.
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
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
