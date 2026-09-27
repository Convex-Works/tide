package wire

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// A URLPolicy says which input and output URLs a machine accepts for a
// service (spec §3): https always, plain http only as far as the policy
// allows.
type URLPolicy struct {
	// LoopbackHTTP allows http to loopback hosts. Machines allow it only
	// for a service whose own base URL is loopback, so that a remote
	// service can't aim requests at services listening only on the
	// owner's machine.
	LoopbackHTTP bool
	// AnyHTTP allows http to any host, as a machine's owner may for
	// development.
	AnyHTTP bool
}

// ServicePolicy is the policy a machine applies to the inputs and outputs
// of the service at baseURL.
func ServicePolicy(baseURL string, allowInsecureHTTP bool) URLPolicy {
	u, err := url.Parse(baseURL)
	return URLPolicy{LoopbackHTTP: err == nil && IsLoopback(u.Hostname()), AnyHTTP: allowInsecureHTTP}
}

// Check fails unless the policy allows rawURL. The error completes the
// sentence "the URL …".
func (p URLPolicy) Check(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("isn't valid: %v", err)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("has no host")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch {
		case p.AnyHTTP:
			return nil
		case !IsLoopback(host):
			return fmt.Errorf("uses http to %s; only https is allowed", host)
		case !p.LoopbackHTTP:
			return fmt.Errorf("uses http to %s, on the machine, for a service that isn't; only https is allowed", host)
		}
		return nil
	}
	return fmt.Errorf("uses %s; only https is allowed", u.Scheme)
}

// CheckPairingPage fails unless page, a confirmation page from the
// service at baseURL, is one machines open (spec §4): an http or https URL
// on the base URL's origin, without a user name or password. The error
// completes the sentence "the confirmation page …".
func CheckPairingPage(baseURL, page string) error {
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%q isn't a web address", page)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("can't be checked against the base URL %q: %v", baseURL, err)
	}
	if origin(u) != origin(base) {
		return fmt.Errorf("%q is on %s, not on %s, the origin of the base URL", page, origin(u), origin(base))
	}
	if u.User != nil {
		return fmt.Errorf("%q has a user name or password in it", page)
	}
	return nil
}

// origin is u's scheme, host and port, written alike for URLs that
// browsers take for the same origin: the host in lower case, an IP address
// in its canonical form, and the default port spelled out.
func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.String()
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}

// IsLoopback reports whether host is localhost, in 127.0.0.0/8, or ::1.
func IsLoopback(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	switch {
	case err != nil:
		return false
	case addr.Is4():
		return addr.As4()[0] == 127
	}
	return addr == netip.IPv6Loopback()
}
