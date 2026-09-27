package wire

import "unicode/utf8"

// IsJobID reports whether s is a valid job ID: ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$.
func IsJobID(s string) bool { return isSegment(s) && len(s) <= 128 }

// IsFileName reports whether s is a valid input or output file name:
// ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$. Such a name is always safe to use as a
// single path component.
func IsFileName(s string) bool { return isSegment(s) && len(s) <= 128 }

// IsMachineID reports whether s is a valid machine ID: 1–64 characters of
// [A-Za-z0-9_-].
func IsMachineID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) && s[i] != '_' && s[i] != '-' {
			return false
		}
	}
	return true
}

// IsAssetName reports whether s is a valid asset name: '/'-separated
// segments, each ^[A-Za-z0-9][A-Za-z0-9._-]*$, at most 256 characters.
func IsAssetName(s string) bool {
	if len(s) > 256 {
		return false
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			if !isSegment(s[start:i]) {
				return false
			}
			start = i + 1
		}
	}
	return true
}

// IsBundleName reports whether s is a valid bundle name: ^[a-z0-9][a-z0-9-]{0,63}$.
func IsBundleName(s string) bool {
	if len(s) < 1 || len(s) > 64 || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// IsSHA256 reports whether s is a SHA-256 written as 64 lowercase hex
// characters.
func IsSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Chars counts the Unicode code points in s, which is how the spec measures
// lengths given in characters.
func Chars(s string) int { return utf8.RuneCountInString(s) }

func isSegment(s string) bool {
	if s == "" || !isAlnum(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}
