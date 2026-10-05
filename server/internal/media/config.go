package media

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/protocol/logger"
)

// devDepartureTimeout is how many seconds a dev server keeps a departed
// participant's room state, against LiveKit's default 20: the media gate
// asserts departures, and 20 seconds raced them.
const devDepartureTimeout = 5

// livekitConfig turns Options into LiveKit's configuration.
//
// The media settings go through LiveKit's own YAML parsing, because
// NewConfig validates them there and decides the node address in the same
// step: set fields afterwards and the address would already be decided.
// Secrets never go through YAML; they are set on the parsed config.
func livekitConfig(opts Options) (*config.Config, error) {
	if opts.APIKey == "" || opts.APISecret == "" {
		return nil, errors.New("media: the API key and secret are required")
	}
	for _, port := range []struct {
		name  string
		value int
	}{
		{"internal", opts.InternalPort},
		{"TCP", opts.TCPPort},
		{"UDP", opts.UDPPort},
	} {
		if port.value < 1 || port.value > 65535 {
			return nil, fmt.Errorf("media: the %s port must be 1-65535, not %d", port.name, port.value)
		}
	}

	var doc strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&doc, format+"\n", args...) }
	line("port: %d", opts.InternalPort)
	// bind_addresses applies to LiveKit's HTTP server (API and signaling)
	// only. Media binds separately: the UDP mux on each interface's address,
	// the ICE/TCP listener on every address.
	line("bind_addresses: [127.0.0.1]")
	line("rtc:")
	line("  tcp_port: %d", opts.TCPPort)
	line("  udp_port: %d", opts.UDPPort)
	switch {
	case opts.NodeIP != "":
		// A configured address replaces the interface addresses in every
		// candidate (LiveKit rewrites them all to node_ip, with no way to
		// keep the internal ones as well), so a recorder in tide's network
		// namespace must reach this address, hairpinning where it isn't
		// local.
		ip := net.ParseIP(opts.NodeIP)
		if ip == nil {
			return nil, fmt.Errorf("media: the node IP %q is not an IP address", opts.NodeIP)
		}
		line("  node_ip: %s", strconv.Quote(ip.String()))
		line("  use_external_ip: false")
		if ip.IsLoopback() {
			loopbackMedia(line)
		}
	case opts.Loopback:
		line("  node_ip: \"127.0.0.1\"")
		line("  use_external_ip: false")
		loopbackMedia(line)
	default:
		// Production's settings: the public address from STUN, advertised
		// alongside the interface addresses rather than in place of them (a
		// recorder in tide's network namespace connects to those), and no
		// self-check of the public address, which fails wherever the network
		// doesn't hairpin (Kubernetes hostPort).
		line("  use_external_ip: true")
		line("  advertise_internal_ip: true")
		line("  skip_external_ip_validation: true")
	}

	conf, err := config.NewConfig(doc.String(), true, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("media: configure: %w", err)
	}
	conf.Keys = map[string]string{opts.APIKey: opts.APISecret}
	if opts.WebhookURL != "" {
		conf.WebHook.APIKey = opts.APIKey
		conf.WebHook.URLs = []string{opts.WebhookURL}
	}
	if opts.Bus != nil {
		conf.Redis.Address = opts.Bus.dialAddr()
		conf.Redis.Password = opts.Bus.Password()
	}
	conf.Logging.Level = "warn"
	if opts.Dev {
		conf.Logging.Level = "info"
		conf.Room.DepartureTimeout = devDepartureTimeout
	}
	if err := conf.ValidateKeys(); err != nil {
		return nil, fmt.Errorf("media: keys: %w", err)
	}
	if err := setLogger(conf); err != nil {
		return nil, err
	}
	return conf, nil
}

// loopbackMedia makes the UDP mux listen on the loopback interface too,
// which it otherwise skips: a 127.0.0.1 candidate needs something there.
func loopbackMedia(line func(string, ...any)) {
	line("  enable_loopback_candidate: true")
}

// loggerOnce guards LiveKit's logger, a package variable it reads without
// synchronization: replacing it while an earlier server's goroutines still
// log is a data race. So the first Start in a process sets it, and its level
// holds for the process; tide starts one server, tests start many.
var (
	loggerOnce sync.Once
	loggerErr  error
)

// setLogger points LiveKit's logger at stderr in its own console format.
// It deliberately doesn't use config.InitLoggerFromConfig: that also makes
// LiveKit's logger slog's default, which would route tide's own log.Print
// lines through it and drop them below its level.
func setLogger(conf *config.Config) error {
	loggerOnce.Do(func() {
		l, err := logger.NewZapLogger(&conf.Logging.Config)
		if err != nil {
			loggerErr = fmt.Errorf("media: logger: %w", err)
			return
		}
		config.SetLogger(l)
	})
	return loggerErr
}
