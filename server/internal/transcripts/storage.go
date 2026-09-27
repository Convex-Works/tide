package transcripts

import (
	"net/netip"
	"net/url"
	"strings"
)

// storageRefused is the error of every transcript while machines would
// refuse the URLs of klisi's storage.
const storageRefused = "Machines can't use klisi's storage: KLISI_S3_PUBLIC_ENDPOINT is plain http, which the moil app accepts only when klisi and its storage run on the machine itself. Ask klisi's administrator to set it to an https address, then try again."

// StorageWarning says why machines would refuse the URLs klisi presigns for
// its storage at publicEndpoint (KLISI_S3_PUBLIC_ENDPOINT), klisi being at
// baseURL, or is "" if they'd take them. The moil app takes https URLs, and
// plain http ones only to storage on the machine itself, from a klisi on
// the machine itself (moil's spec §3), as in development. Set as
// Config.StorageProblem, it fails every transcript at once.
func StorageWarning(baseURL, publicEndpoint string) string {
	storage, err := url.Parse(publicEndpoint)
	if err == nil && storage.Scheme == "https" {
		return ""
	}
	klisi, klisiErr := url.Parse(baseURL)
	if err == nil && klisiErr == nil && isLoopback(storage.Hostname()) && isLoopback(klisi.Hostname()) {
		return ""
	}
	return storageRefused
}

// isLoopback reports whether host is localhost, in 127.0.0.0/8, or ::1, as
// the moil app decides.
func isLoopback(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if addr.Is4() {
		return addr.As4()[0] == 127
	}
	return addr == netip.IPv6Loopback()
}
