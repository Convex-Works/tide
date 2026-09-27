package moil

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/BurntSushi/toml"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// A Package is a package a bundle's lockfile pins (spec §5.1), as machine
// owners see it when they review the bundle.
type Package struct {
	Name string
	// Version is the pinned version, or "" if the lockfile doesn't give
	// one.
	Version string
	// SourceKind says where the package comes from: "registry" (a package
	// index), "url" (an archive or a wheel) or "git" (a repository pinned
	// to a full commit). Source is its https URL.
	SourceKind string
	Source     string
	// BuildsFromSource reports that the lockfile has no wheel for the
	// package, so machines build it from source. Its build requirements
	// are resolved when it's built rather than pinned by the lockfile,
	// which is why machines point it out to their owners.
	BuildsFromSource bool
}

// parseLockfile parses job.py.lock, checks that it pins everything it can
// (spec §5.1), and lists its packages. The error says what's wrong, for
// the bundle's author.
func parseLockfile(text string) ([]Package, error) {
	var lock map[string]any
	if _, err := toml.Decode(text, &lock); err != nil {
		return nil, fmt.Errorf("moil: job.py.lock isn't valid TOML: %w", err)
	}
	raw, ok := lock["package"]
	if !ok {
		return nil, nil
	}
	tables, ok := asArray(raw)
	if !ok {
		return nil, errors.New("moil: job.py.lock isn't valid: `package` must be an array of tables")
	}
	packages := make([]Package, 0, len(tables))
	for _, t := range tables {
		p, err := lockedPackage(t)
		if err != nil {
			return nil, fmt.Errorf("moil: job.py.lock isn't valid: %w", err)
		}
		packages = append(packages, p)
	}
	return packages, nil
}

func lockedPackage(value any) (Package, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return Package{}, errors.New("every `[[package]]` must be a table")
	}
	name, ok := table["name"].(string)
	if !ok {
		return Package{}, errors.New("a package has no name")
	}
	p := Package{Name: name}
	p.Version, _ = table["version"].(string)
	fail := func(format string, args ...any) (Package, error) {
		return Package{}, fmt.Errorf("package %s: "+format, append([]any{name}, args...)...)
	}

	source, ok := table["source"].(map[string]any)
	if !ok {
		return fail("it has no source")
	}
	var err error
	if p.SourceKind, p.Source, err = packageSource(source); err != nil {
		return fail("%v", err)
	}

	sdist, hasSdist := table["sdist"]
	var wheels []any
	if w, ok := table["wheels"]; ok {
		if wheels, ok = asArray(w); !ok {
			return fail("`wheels` must be an array")
		}
	}
	artifacts := wheels
	if hasSdist {
		artifacts = append([]any{sdist}, wheels...)
	}
	for i, artifact := range artifacts {
		fields, _ := artifact.(map[string]any)
		hash, _ := fields["hash"].(string)
		if digest, ok := strings.CutPrefix(hash, "sha256:"); ok && wire.IsSHA256(digest) {
			continue
		}
		what, ok := fields["url"].(string)
		switch {
		case ok:
		case hasSdist && i == 0:
			what = "its sdist"
		default:
			what = "a wheel"
		}
		return fail("%s has no SHA-256 hash, so it isn't pinned", what)
	}
	if len(artifacts) == 0 && p.SourceKind != "git" {
		return fail("it lists nothing to install")
	}
	p.BuildsFromSource = len(wheels) == 0
	return p, nil
}

// packageSource checks where a package comes from: an https package
// index, an https URL, or a git repository over https pinned to a full
// commit.
func packageSource(source map[string]any) (kind, location string, err error) {
	get := func(key string) (string, bool) {
		s, ok := source[key].(string)
		return s, ok
	}
	if index, ok := get("registry"); ok {
		return "registry", index, isHTTPS(index, "its package index")
	}
	if u, ok := get("url"); ok {
		return "url", u, isHTTPS(u, "its URL")
	}
	if repo, ok := get("git"); ok {
		if err := isHTTPS(repo, "its git repository"); err != nil {
			return "", "", err
		}
		commit := ""
		if i := strings.LastIndexByte(repo, '#'); i >= 0 {
			commit = repo[i+1:]
		}
		if len(commit) != 40 || !isHex(commit) {
			return "", "", fmt.Errorf("its git source %s isn't pinned to a full commit", repo)
		}
		return "git", repo, nil
	}
	for _, key := range []string{"path", "directory", "editable", "virtual"} {
		if path, ok := get(key); ok {
			return "", "", fmt.Errorf("it comes from %s, a path on the author's machine that other machines don't have", path)
		}
	}
	return "", "", fmt.Errorf("its source %v isn't one moil knows", source)
}

func isHTTPS(rawURL, what string) error {
	if u, err := url.Parse(rawURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s, %s, isn't https", what, rawURL)
	}
	return nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

// asArray returns a TOML array's elements, whether it was written as an
// array of tables or inline.
func asArray(v any) ([]any, bool) {
	switch a := v.(type) {
	case []any:
		return a, true
	case []map[string]any:
		out := make([]any, len(a))
		for i, t := range a {
			out[i] = t
		}
		return out, true
	}
	return nil, false
}
