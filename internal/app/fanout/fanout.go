package fanout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fedor-git/wg-portal-2/internal/app/wireguard"
	cfgpkg "github.com/fedor-git/wg-portal-2/internal/config"
)

type EventBus interface {
	Subscribe(topic string, fn interface{}) error
}

const (
	hdrOrigin = "X-WGP-Origin"
	hdrNoEcho = "X-WGP-NoEcho"
	hdrCorrID = "X-WGP-Correlation-ID"
)

type settings struct {
	Enabled           bool
	Peers             []string
	AuthHeader        string
	AuthValue         string
	Timeout           time.Duration
	Debounce          time.Duration
	SelfURL           string
	Origin            string
	KickOnStart       bool
	Topics            []string
	TLSSkipVerify     bool
	TLSClientCertFile string
	TLSClientKeyFile  string
	TLSCACertFile     string
}

type MetricsServerHealthUpdater interface {
	SetSyncStatus(ok bool, errMsg string)
}

func Start(ctx context.Context, bus EventBus, fc cfgpkg.FanoutConfig, wireGuardManager *wireguard.Manager, collector *wireguard.StatisticsCollector, metricsServer MetricsServerHealthUpdater) {
	s := settings{
		Enabled:           fc.Enabled,
		Peers:             append([]string(nil), fc.Peers...),
		AuthHeader:        fc.AuthHeader,
		AuthValue:         fc.AuthValue,
		Timeout:           fc.Timeout,
		Debounce:          fc.Debounce,
		SelfURL:           fc.SelfURL,
		Origin:            fc.Origin,
		KickOnStart:       fc.KickOnStart,
		Topics:            append([]string(nil), fc.Topics...),
		TLSSkipVerify:     fc.TLSSkipVerify,
		TLSClientCertFile: fc.TLSClientCertFile,
		TLSClientKeyFile:  fc.TLSClientKeyFile,
		TLSCACertFile:     fc.TLSCACertFile,
	}

	if !s.Enabled {
		slog.Info("[FANOUT] disabled, skip init")
		return
	}
	if s.Timeout <= 0 {
		// 60 second timeout for sync endpoint to accommodate large peer counts
		// (241 peers with database updates + potential retries can exceed 5 seconds)
		s.Timeout = 60 * time.Second
	}
	if s.Debounce <= 0 {
		s.Debounce = 250 * time.Millisecond
	}

	if s.Origin == "" {
		if host := getHost(s.SelfURL); host != "" {
			if hn, _ := net.LookupAddr(host); len(hn) > 0 {
				s.Origin = strings.TrimSuffix(hn[0], ".")
			}
			if s.Origin == "" {
				s.Origin = host
			}
		}
		if s.Origin == "" {
			s.Origin = "unknown-node"
		}
	}

	if len(s.Topics) == 0 {
		// Peer events are handled by event-driven sync handlers (see wireguard.go)
		// Fanout only needs interface events for operations that affect all peers (e.g., config changes)
		s.Topics = []string{
			"interface:created", "interface:updated", "interface:deleted",
		}
	}

	f := &fanout{
		cfg:           s,
		client:        createHTTPClient(s),
		debounce:      newDebouncer(s.Debounce),
		metricsServer: metricsServer,
	}
	if bus != nil {
		for _, topic := range s.Topics {
			t := topic
			if err := bus.Subscribe(t, func(arg any) {
				slog.Debug("[FANOUT] bump", "reason", "bus:"+t, "arg", arg)
				f.bump("bus:" + t)
				// Removed: SyncAllPeersFromDB() - now using event-driven sync instead
				// Each node listens to peer:created, peer:updated, peer:deleted events directly
			}); err != nil {
				slog.Warn("[FANOUT] subscribe failed", "topic", t, "err", err)
			} else {
				slog.Debug("[FANOUT] subscribed", "topic", t)
			}
		}
	} else {
		slog.Debug("[FANOUT] no bus provided, event-driven bumps disabled")
	}

	go f.loop(ctx)

	if s.KickOnStart {
		f.bump("startup")
	}

	authConfigured := s.AuthHeader != "" && s.AuthValue != ""
	slog.Info("[FANOUT] initialized",
		"peers", len(s.Peers),
		"topics", s.Topics,
		"self", s.SelfURL,
		"origin", s.Origin,
		"debounce", s.Debounce.String(),
		"timeout", s.Timeout.String(),
		"auth_configured", authConfigured,
	)
}

func createHTTPClient(s settings) *http.Client {
	transport := &http.Transport{}

	if s.TLSSkipVerify || s.TLSCACertFile != "" || s.TLSClientCertFile != "" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: s.TLSSkipVerify,
		}

		if s.TLSCACertFile != "" {
			caCert, err := os.ReadFile(s.TLSCACertFile)
			if err != nil {
				slog.Warn("[FANOUT] failed to read CA certificate", "file", s.TLSCACertFile, "err", err)
			} else {
				caCertPool := x509.NewCertPool()
				if caCertPool.AppendCertsFromPEM(caCert) {
					tlsConfig.RootCAs = caCertPool
					slog.Debug("[FANOUT] loaded CA certificate", "file", s.TLSCACertFile)
				} else {
					slog.Warn("[FANOUT] failed to parse CA certificate", "file", s.TLSCACertFile)
				}
			}
		}

		if s.TLSClientCertFile != "" && s.TLSClientKeyFile != "" {
			cert, err := tls.LoadX509KeyPair(s.TLSClientCertFile, s.TLSClientKeyFile)
			if err != nil {
				slog.Warn("[FANOUT] failed to load client certificate",
					"cert", s.TLSClientCertFile, "key", s.TLSClientKeyFile, "err", err)
			} else {
				tlsConfig.Certificates = []tls.Certificate{cert}
				slog.Debug("[FANOUT] loaded client certificate",
					"cert", s.TLSClientCertFile, "key", s.TLSClientKeyFile)
			}
		}

		transport.TLSClientConfig = tlsConfig
	}

	return &http.Client{
		Timeout:   s.Timeout,
		Transport: transport,
	}
}

type fanout struct {
	cfg           settings
	client        *http.Client
	debounce      *debouncer
	metricsServer MetricsServerHealthUpdater
}

func (f *fanout) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.debounce.C:
			f.fire(ctx)
		}
	}
}

func (f *fanout) bump(reason string) {
	slog.Debug("[FANOUT] bump", "reason", reason)
	f.debounce.Bump()
}

func (f *fanout) fire(ctx context.Context) {
	var wg sync.WaitGroup
	var failedPeers []string
	var failedMutex sync.Mutex

	for _, base := range f.cfg.Peers {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		if isSelf(base, f.cfg.SelfURL) {
			continue
		}
		endpoint := strings.TrimRight(base, "/") + "/api/v1/peer/sync"

		wg.Add(1)
		go func(u string) {
			defer wg.Done()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
			if err != nil {
				slog.Warn("[FANOUT] build request failed", "url", u, "err", err)
				failedMutex.Lock()
				failedPeers = append(failedPeers, u)
				failedMutex.Unlock()
				return
			}

			if f.cfg.AuthHeader != "" && f.cfg.AuthValue != "" {
				req.Header.Set(f.cfg.AuthHeader, f.cfg.AuthValue)
				slog.Debug("[FANOUT] adding auth header", "header", f.cfg.AuthHeader, "url", u)
			} else {
				slog.Warn("[FANOUT] no auth configured for request", "url", u)
			}

			req.Header.Set(hdrOrigin, f.cfg.Origin)
			req.Header.Set(hdrNoEcho, "1")
			req.Header.Set(hdrCorrID, uuid.NewString())

			resp, err := f.client.Do(req)
			if err != nil {
				slog.Warn("[FANOUT] sync failed", "url", u, "err", err)
				failedMutex.Lock()
				failedPeers = append(failedPeers, u)
				failedMutex.Unlock()
				return
			}
			_ = resp.Body.Close()

			if resp.StatusCode >= 300 {
				slog.Warn("[FANOUT] sync non-2xx", "url", u, "status", resp.Status)
				failedMutex.Lock()
				failedPeers = append(failedPeers, u+" ("+resp.Status+")")
				failedMutex.Unlock()
				return
			}
			slog.Debug("[FANOUT] sync ok", "url", u)
		}(endpoint)
	}

	wg.Wait()

	// Update health status based on sync results
	if len(failedPeers) > 0 {
		errMsg := "Sync failed for peers: " + strings.Join(failedPeers, ", ")
		f.metricsServer.SetSyncStatus(false, errMsg)
	} else {
		f.metricsServer.SetSyncStatus(true, "")
	}
}

type debouncer struct {
	d   time.Duration
	tmu sync.Mutex
	t   *time.Timer
	C   chan struct{}
}

func newDebouncer(d time.Duration) *debouncer {
	return &debouncer{
		d: d,
		C: make(chan struct{}, 1),
	}
}

func (d *debouncer) Bump() {
	d.tmu.Lock()
	defer d.tmu.Unlock()

	if d.t == nil {
		d.t = time.AfterFunc(d.d, func() {
			select {
			case d.C <- struct{}{}:
			default:
			}
			d.reset()
		})
		return
	}

	if !d.t.Stop() {
		select {
		case <-d.C:
		default:
		}
	}
	d.t.Reset(d.d)
}

func (d *debouncer) reset() {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.t = nil
}

func isSelf(targetBase, selfBase string) bool {
	if selfBase == "" || targetBase == "" {
		return false
	}

	tu, terr := url.Parse(strings.TrimSpace(targetBase))
	su, serr := url.Parse(strings.TrimSpace(selfBase))
	if terr != nil || serr != nil {
		return strings.TrimRight(targetBase, "/") == strings.TrimRight(selfBase, "/")
	}

	ts := strings.ToLower(tu.Scheme)
	ss := strings.ToLower(su.Scheme)
	if ts == "" {
		ts = "http"
	}
	if ss == "" {
		ss = "http"
	}

	th := normalizeHostPort(tu.Host, ts)
	sh := normalizeHostPort(su.Host, ss)

	return ts == ss && th == sh
}

func normalizeHostPort(host, scheme string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	if !strings.Contains(h, ":") {
		switch scheme {
		case "https":
			h += ":443"
		default:
			h += ":80"
		}
	}
	return h
}

func getHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	h := u.Host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}
