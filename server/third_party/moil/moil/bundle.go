package moil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// The files of a version 1 bundle (spec §5.1), in the order they're hashed.
const (
	ScriptFile   = "job.py"
	LockFile     = "job.py.lock"
	ManifestFile = "manifest.json"
)

// MaxBundleBytes is the most a bundle's three files may add up to.
const MaxBundleBytes = 8 << 20

var bundleFiles = [...]string{ScriptFile, LockFile, ManifestFile}

// A Bundle is the code machines run for a service: job.py, job.py.lock and
// manifest.json, identified by their hash (spec §5.2).
//
// A Bundle can only be made from valid files, so holding one means the
// manifest, the lockfile and the hash have been checked. It is immutable
// and safe to share.
type Bundle struct {
	hash     string
	manifest Manifest
	packages []Package
	files    [len(bundleFiles)][]byte
}

// LoadBundle reads a bundle's three files from the root of fsys, such as
// os.DirFS("bundles/transcribe") or a subtree of an embed.FS, and checks
// them. Other files in fsys are ignored, so a bundle's source directory can
// hold a README or tests.
func LoadBundle(fsys fs.FS) (*Bundle, error) {
	files := make(map[string][]byte, len(bundleFiles))
	for _, name := range bundleFiles {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("moil: the bundle has no %s", name)
			}
			return nil, fmt.Errorf("moil: reading the bundle: %w", err)
		}
		files[name] = data
	}
	return NewBundle(files)
}

// NewBundle makes a bundle from its files by name. files must hold exactly
// job.py, job.py.lock and manifest.json.
func NewBundle(files map[string][]byte) (*Bundle, error) {
	var b Bundle
	size := 0
	for i, name := range bundleFiles {
		data, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("moil: the bundle has no %s", name)
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("moil: %s isn't UTF-8 text", name)
		}
		b.files[i] = bytes.Clone(data)
		size += len(data)
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !slices.Contains(bundleFiles[:], name) {
			return nil, fmt.Errorf("moil: a bundle holds only job.py, job.py.lock and manifest.json, not %q", name)
		}
	}
	if size > MaxBundleBytes {
		return nil, fmt.Errorf("moil: the bundle is %d bytes; the limit is 8 MiB", size)
	}
	m, err := ParseManifest(b.files[2])
	if err != nil {
		return nil, err
	}
	b.manifest = *m
	if b.packages, err = parseLockfile(string(b.files[1])); err != nil {
		return nil, err
	}
	b.hash = hashFiles(b.files)
	return &b, nil
}

// hashFiles computes the bundle hash: the SHA-256 of the sha256sum listing
// of the files, in bytewise order of name.
func hashFiles(files [len(bundleFiles)][]byte) string {
	listing := sha256.New()
	for i, name := range bundleFiles {
		sum := sha256.Sum256(files[i])
		fmt.Fprintf(listing, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return hex.EncodeToString(listing.Sum(nil))
}

// Hash is the bundle hash, 64 lowercase hex characters. It changes with any
// byte of any file, and it's what machine owners approve.
func (b *Bundle) Hash() string { return b.hash }

// Name is the bundle name from the manifest. Versions of a bundle share it.
func (b *Bundle) Name() string { return b.manifest.Name }

// Version is the manifest's free-form version label.
func (b *Bundle) Version() string { return b.manifest.Version }

// Description is the manifest's description, or "".
func (b *Bundle) Description() string { return b.manifest.Description }

// Manifest returns the parsed manifest.
func (b *Bundle) Manifest() Manifest {
	m := b.manifest
	m.Assets = maps.Clone(m.Assets)
	return m
}

// Packages returns the packages the bundle's lockfile pins, in lockfile
// order.
func (b *Bundle) Packages() []Package { return slices.Clone(b.packages) }

// File returns a copy of the named bundle file's exact bytes, or nil if name
// isn't one of the three bundle files.
func (b *Bundle) File(name string) []byte {
	i := slices.Index(bundleFiles[:], name)
	if i < 0 {
		return nil
	}
	return bytes.Clone(b.files[i])
}

// String identifies the bundle for logs: name, version and short hash.
func (b *Bundle) String() string {
	return fmt.Sprintf("%s %s (%s)", b.manifest.Name, b.manifest.Version, b.hash[:12])
}

func (b *Bundle) info() wire.BundleInfo {
	return wire.BundleInfo{Hash: b.hash, Name: b.manifest.Name, Version: b.manifest.Version, Description: b.manifest.Description}
}

func (b *Bundle) response() wire.BundleResponse {
	files := make(map[string]string, len(bundleFiles))
	for i, name := range bundleFiles {
		files[name] = string(b.files[i])
	}
	return wire.BundleResponse{Hash: b.hash, Files: files}
}

// A Manifest is a bundle's manifest.json (spec §5.3).
type Manifest struct {
	// Moil is the manifest format version, always 1.
	Moil        int
	Name        string
	Version     string
	Description string
	// Assets are the files the runtime downloads and verifies for the
	// script, by asset name.
	Assets    map[string]Asset
	Resources Resources
}

// An Asset is a file the runtime downloads, verifies and hands to the
// script.
type Asset struct {
	URL    string
	SHA256 string
	// SizeBytes is the asset's size, or 0 when the manifest doesn't say.
	SizeBytes uint64
}

// Resources is what a bundle says it needs. It is informational in v1.
type Resources struct {
	// MemoryBytes is 0 when the manifest doesn't say.
	MemoryBytes uint64
	// GPU is "none", "optional", "required", or "" when the manifest
	// doesn't say.
	GPU string
}

// ParseManifest parses and checks a manifest.json. Manifests are strict:
// unknown fields and duplicate keys, at any level, make it invalid, because
// the owner approving a bundle must see exactly what the machine will act
// on.
func ParseManifest(data []byte) (*Manifest, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("moil: manifest.json isn't UTF-8 text")
	}
	if err := checkManifestShape(data); err != nil {
		return nil, fmt.Errorf("moil: manifest.json isn't valid: %w", err)
	}
	var raw struct {
		Moil        int     `json:"moil"`
		Name        string  `json:"name"`
		Version     string  `json:"version"`
		Description *string `json:"description"`
		Assets      map[string]struct {
			URL       string  `json:"url"`
			SHA256    string  `json:"sha256"`
			SizeBytes *uint64 `json:"size_bytes"`
		} `json:"assets"`
		Resources struct {
			MemoryBytes *uint64 `json:"memory_bytes"`
			GPU         *string `json:"gpu"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("moil: manifest.json isn't valid: %w", err)
	}
	invalid := func(format string, args ...any) (*Manifest, error) {
		return nil, fmt.Errorf("moil: manifest.json isn't valid: "+format, args...)
	}
	if raw.Moil != 1 {
		return invalid(`"moil" must be 1, not %d`, raw.Moil)
	}
	if !wire.IsBundleName(raw.Name) {
		return invalid(`name %q must be 1–64 characters of a-z, 0-9 and '-', starting with a letter or digit`, raw.Name)
	}
	if n := wire.Chars(raw.Version); n < 1 || n > 32 {
		return invalid("version must be 1–32 characters")
	}
	if !isPlainText(raw.Version, false) {
		return invalid(hiddenCharacters, "version")
	}
	m := &Manifest{Moil: raw.Moil, Name: raw.Name, Version: raw.Version}
	if raw.Description != nil {
		if wire.Chars(*raw.Description) > 500 {
			return invalid("description must be at most 500 characters")
		}
		if !isPlainText(*raw.Description, true) {
			return invalid(hiddenCharacters, "description")
		}
		m.Description = *raw.Description
	}
	if len(raw.Assets) > 0 {
		m.Assets = make(map[string]Asset, len(raw.Assets))
	}
	for name, a := range raw.Assets {
		if !wire.IsAssetName(name) {
			return invalid("asset name %q must be a relative path of segments made of A-Z, a-z, 0-9, '.', '_' and '-'", name)
		}
		if !isAssetURL(a.URL) {
			return invalid("asset %q must be fetched from a plain https URL (https://host[:port]/path[?query]), not %q", name, a.URL)
		}
		if !wire.IsSHA256(a.SHA256) {
			return invalid("asset %q has an invalid sha256 (expected 64 lowercase hex characters)", name)
		}
		asset := Asset{URL: a.URL, SHA256: a.SHA256}
		if a.SizeBytes != nil {
			asset.SizeBytes = *a.SizeBytes
		}
		m.Assets[name] = asset
	}
	if file, nested, ok := assetLayoutConflict(m.Assets); ok {
		return invalid("assets %q and %q can't both exist: %q would have to be a file and a directory", file, nested, file)
	}
	if raw.Resources.MemoryBytes != nil {
		m.Resources.MemoryBytes = *raw.Resources.MemoryBytes
	}
	if gpu := raw.Resources.GPU; gpu != nil {
		switch *gpu {
		case "none", "optional", "required":
			m.Resources.GPU = *gpu
		default:
			return invalid(`resources.gpu must be "none", "optional" or "required", not %q`, *gpu)
		}
	}
	return m, nil
}

const hiddenCharacters = "%s contains control or bidirectional formatting characters, which could make it read differently from what it is"

// isPlainText reports whether s has no control characters, except line
// feeds if lineFeeds, and no bidirectional formatting characters (spec
// §5.3).
func isPlainText(s string, lineFeeds bool) bool {
	for _, r := range s {
		if (unicode.IsControl(r) && !(lineFeeds && r == '\n')) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// isAssetURL reports whether s is an asset URL as spec §5.3 defines it:
// https:// host [":" port] [path] ["?" query], where the host is a DNS
// name or an IPv4 address, and the path and query use only unreserved
// characters, percent-encodings, sub-delimiters, ":", "@", "/" and "?".
// The grammar is narrower than what URL parsers accept, so that every
// implementation agrees on which manifests are valid.
func isAssetURL(s string) bool {
	rest, ok := strings.CutPrefix(s, "https://")
	if !ok {
		return false
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	host, port, hasPort := strings.Cut(rest[:end], ":")
	if host == "" {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || strings.ContainsFunc(label, func(r rune) bool { return !isASCIIAlnum(r) && r != '-' }) {
			return false
		}
	}
	if hasPort {
		n, err := strconv.Atoi(port)
		digits := !strings.ContainsFunc(port, func(r rune) bool { return r < '0' || r > '9' })
		if !digits || len(port) > 5 || err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return isPathAndQuery(rest[end:])
}

// isPathAndQuery reports whether s uses only RFC 3986 unreserved
// characters, percent-encodings, sub-delimiters, ":", "@", "/" and "?".
func isPathAndQuery(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '%':
			if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
				return false
			}
			i += 2
		case isASCIIAlnum(rune(c)) || strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isASCIIAlnum(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// assetLayoutConflict finds two assets that can't both be laid out on disk,
// because one would have to be a file and a directory at once (a and a/b).
func assetLayoutConflict(assets map[string]Asset) (file, nested string, ok bool) {
	for name := range assets {
		for dir := name; ; {
			i := strings.LastIndexByte(dir, '/')
			if i < 0 {
				break
			}
			dir = dir[:i]
			if _, clash := assets[dir]; clash {
				return dir, name, true
			}
		}
	}
	return "", "", false
}

// A shape says which keys a JSON object may have. A nil *shape accepts any
// value.
type shape struct {
	fields map[string]*shape // an object with at most these keys
	values *shape            // an object with any keys, whose values have this shape
}

var scalar = &shape{}

var manifestShape = &shape{fields: map[string]*shape{
	"moil": scalar, "name": scalar, "version": scalar, "description": scalar,
	"assets": {values: &shape{fields: map[string]*shape{
		"url": scalar, "sha256": scalar, "size_bytes": scalar,
	}}},
	"resources": {fields: map[string]*shape{"memory_bytes": scalar, "gpu": scalar}},
}}

// checkManifestShape rejects what encoding/json would let through: a
// document that isn't an object, duplicate keys, unknown keys (which
// encoding/json would also match case-insensitively), null where an object
// belongs, escapes of unpaired surrogates (which it would replace with
// U+FFFD), and trailing data.
func checkManifestShape(data []byte) error {
	w := &walker{dec: json.NewDecoder(bytes.NewReader(data)), data: data}
	w.dec.UseNumber()
	if err := w.walk(manifestShape, "manifest"); err != nil {
		return err
	}
	if _, err := w.dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the manifest object")
	}
	return nil
}

// A walker reads a JSON document token by token.
type walker struct {
	dec  *json.Decoder
	data []byte
}

// token returns the next token, refusing strings that escape half of a
// UTF-16 surrogate pair, which encodes no character.
func (w *walker) token() (json.Token, error) {
	start := w.dec.InputOffset()
	tok, err := w.dec.Token()
	if err != nil {
		return nil, err
	}
	// Between two tokens there's only whitespace and punctuation, so every
	// escape in the span is in the string.
	if _, ok := tok.(string); ok && hasLoneSurrogate(w.data[start:w.dec.InputOffset()]) {
		return nil, fmt.Errorf("the string %q escapes half of a surrogate pair, which isn't a character", tok)
	}
	return tok, nil
}

// hasLoneSurrogate reports whether raw JSON text holds a \u escape of a
// surrogate that isn't followed or preceded by its other half.
func hasLoneSurrogate(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		r, ok := unicodeEscape(raw[i:])
		if !ok {
			i++ // skip the escaped character, which may be a backslash
			continue
		}
		i += 5 // the escape's last hex digit
		switch {
		case r < 0xD800 || r > 0xDFFF:
		case r >= 0xDC00:
			return true // a low surrogate without a high one before it
		default:
			if low, ok := unicodeEscape(raw[i+1:]); !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 6
		}
	}
	return false
}

// unicodeEscape decodes the \uXXXX escape at the start of b.
func unicodeEscape(b []byte) (rune, bool) {
	if len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b[2:6]), 16, 16)
	return rune(n), err == nil
}

func (w *walker) walk(s *shape, path string) error {
	tok, err := w.token()
	if err != nil {
		return err
	}
	isObject := s != nil && (s.fields != nil || s.values != nil)
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		if isObject {
			return fmt.Errorf("%s must be a JSON object", path)
		}
		if ok && delim == '[' {
			for w.dec.More() {
				if err := w.walk(nil, path+"[]"); err != nil {
					return err
				}
			}
			_, err = w.dec.Token()
		}
		return err
	}
	seen := make(map[string]bool)
	for w.dec.More() {
		tok, err := w.token()
		if err != nil {
			return err
		}
		key := tok.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q in %s", key, path)
		}
		seen[key] = true
		var child *shape
		switch {
		case s != nil && s.fields != nil:
			if child, ok = s.fields[key]; !ok {
				return fmt.Errorf("unknown field %q in %s", key, path)
			}
			if child == scalar {
				child = nil
			}
		case s != nil && s.values != nil:
			child = s.values
		}
		if err := w.walk(child, path+"."+key); err != nil {
			return err
		}
	}
	_, err = w.dec.Token()
	return err
}
