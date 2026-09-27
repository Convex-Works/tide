package moiltest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// reencode returns a JSON value the way a machine passes it on, params to
// the script and payloads and meta to the service: the app parses the
// value and writes it again, with object keys in order, no whitespace,
// integers that fit 64 bits as they are, and every other number as the
// shortest decimal that reads back as the same double. So a service's
// tests see what it will see from real machines: the same JSON value,
// not the same bytes (spec §2). Its error completes the sentence "the
// value …".
func reencode(raw []byte) (json.RawMessage, error) {
	if err := wire.CheckValue(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeValue(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeValue(out *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case map[string]any:
		out.WriteByte('{')
		for i, key := range slices.Sorted(maps.Keys(v)) {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, key)
			out.WriteByte(':')
			if err := writeValue(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeValue(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		writeString(out, v)
	case json.Number:
		n, err := formatNumber(v)
		if err != nil {
			return err
		}
		out.WriteString(n)
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case nil:
		out.WriteString("null")
	}
	return nil
}

// writeString writes s as serde_json does, escaping only quotes,
// backslashes and control characters.
func writeString(out *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if c < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[c>>4])
				out.WriteByte(hex[c&0xf])
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('"')
}

// formatNumber writes a number as serde_json does: an integer that fits
// an int64 or a uint64 as it is, anything else as a double.
func formatNumber(n json.Number) (string, error) {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") && s != "-0" {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return strconv.FormatInt(i, 10), nil
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return strconv.FormatUint(u, 10), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", err
	}
	return formatDouble(f), nil
}

// formatDouble writes f as the shortest decimal that reads back as f, in
// serde_json's notation: 1.0, 0.001234, 12340000000.0, 1e-7, 1e+30 and
// 1.234e+33.
func formatDouble(f float64) string {
	// d.ddde±x: the shortest digits, and the exponent of the first.
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if sci[0] == '-' {
		sign, sci = "-", sci[1:]
	}
	mantissa, exp, _ := strings.Cut(sci, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	if strings.Trim(digits, "0") == "" {
		return sign + "0.0"
	}
	e, _ := strconv.Atoi(exp)
	n := len(digits)
	point := e + 1 // digits[:point] is the integer part: 10^(point-1) <= |f| < 10^point
	switch {
	case point >= n && point <= 16:
		return sign + digits + strings.Repeat("0", point-n) + ".0"
	case point > 0 && point <= 16:
		return sign + digits[:point] + "." + digits[point:]
	case point > -5 && point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	}
	exponent := fmt.Sprintf("e%+d", point-1)
	if n == 1 {
		return sign + digits + exponent
	}
	return sign + digits[:1] + "." + digits[1:] + exponent
}
