// Package wireguard provides statistics collection and monitoring functionality
// for WireGuard interfaces and peers. It handles real-time data collection,
// ping checks, and metrics reporting to monitoring systems like Prometheus.
package wireguard

import (
	"context"
	"errors"
	"fmt"

	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

// CleanOrphanPeerMetrics removes metrics for peers that no longer exist in the database.
func (c *StatisticsCollector) CleanOrphanPeerMetrics(ctx context.Context) {
       dbPeers, err := c.db.GetAllPeers(ctx)
       if err != nil {
	       slog.Warn("failed to fetch all peers for metrics cleanup", "error", err)
	       return
       }
       dbPeerIDs := make(map[domain.PeerIdentifier]struct{}, len(dbPeers))
       for _, p := range dbPeers {
	       dbPeerIDs[p.Identifier] = struct{}{}
       }

       c.Lock()
       for peerID := range c.metricsLabels {
	       if _, exists := dbPeerIDs[peerID]; !exists {
		       slog.Info("Removing orphan metrics for peer", "peerID", peerID)
		       c.ms.RemovePeerMetrics(&domain.Peer{Identifier: peerID})
		       delete(c.metricsLabels, peerID)
	       }
       }
       c.Unlock()
}

// StatisticsDatabaseRepo defines the database operations required for statistics collection.
type StatisticsDatabaseRepo interface {
	GetAllInterfaces(ctx context.Context) ([]domain.Interface, error)
	GetInterfacePeers(ctx context.Context, id domain.InterfaceIdentifier) ([]domain.Peer, error)
	GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error)
	GetAllPeers(ctx context.Context) ([]domain.Peer, error)
	UpdatePeerStatus(
		ctx context.Context,
		id domain.PeerIdentifier,
		updateFunc func(in *domain.PeerStatus) (*domain.PeerStatus, error),
	) error
	UpdateInterfaceStatus(
		ctx context.Context,
		id domain.InterfaceIdentifier,
		updateFunc func(in *domain.InterfaceStatus) (*domain.InterfaceStatus, error),
	) error
	DeletePeerStatus(ctx context.Context, id domain.PeerIdentifier) error
}

// StatisticsMetricsServer defines the interface for updating metrics in the monitoring system (e.g., Prometheus).
type StatisticsMetricsServer interface {
	UpdateInterfaceMetrics(status domain.InterfaceStatus)
	UpdatePeerMetrics(peer *domain.Peer, status domain.PeerStatus)
	RemovePeerMetrics(peer *domain.Peer)
}

// StatisticsEventBus defines the interface for event-driven communication within the application.
type StatisticsEventBus interface {
	// Subscribe subscribes to a topic
	Subscribe(topic string, fn interface{}) error
	// Publish sends a message to the message bus.
	Publish(topic string, args ...any)
}

// pingJob represents a ping check task for a specific peer.
type pingJob struct {
	Peer    domain.Peer
	Backend domain.InterfaceBackend
}

// StatisticsCollector manages the collection and reporting of WireGuard statistics.
// It handles peer and interface monitoring, ping checks, and metrics updates.
type StatisticsCollector struct {
	pingWaitGroup   sync.WaitGroup                            // Synchronization for ping workers
	db              StatisticsDatabaseRepo                    // Database interface for statistics storage
	cfg             *config.Config                           // Application configuration
	bus             StatisticsEventBus                       // Event bus for real-time notifications
	pingJobs        chan pingJob                             // Channel for ping job queue
	wg              *ControllerManager                       // WireGuard controller manager
	ms              StatisticsMetricsServer                  // Metrics server interface (Prometheus)
	peerChangeEvent chan domain.PeerIdentifier               // Channel for peer change notifications
	metricsLabels   map[domain.PeerIdentifier][]string       // Cached metrics labels for each peer
	sync.RWMutex                                             // Read-write mutex for thread safety
	peerCache       map[domain.PeerIdentifier]*domain.Peer   // Cached peer data for performance
	peerPingSuccess map[domain.PeerIdentifier]bool           // Track successful ping status per peer
}

// getPeerPingAddress extracts the first IP address from a peer's AllowedIPs for ping checks.
// It falls back to CheckAliveAddress if explicitly set in the interface configuration.
func getPeerPingAddress(peer domain.Peer) string {
	// First check if CheckAliveAddress is explicitly set
	if peer.Interface.CheckAliveAddress != "" {
		return peer.Interface.CheckAliveAddress
	}
	
	// Parse AllowedIPsStr to get the first IP
	allowedIPsStr := peer.AllowedIPsStr.GetValue()
	if allowedIPsStr == "" {
		return ""
	}
	
	// Split by comma and get first IP
	ips := strings.Split(allowedIPsStr, ",")
	if len(ips) == 0 {
		return ""
	}
	
	// Remove CIDR suffix (/32, /24, etc.) to get just the IP
	firstIP := strings.TrimSpace(ips[0])
	if strings.Contains(firstIP, "/") {
		parts := strings.Split(firstIP, "/")
		firstIP = parts[0]
	}
	
	return firstIP
}

// getPeerIDs extracts peer identifiers from a slice of peers for debug logging.
func getPeerIDs(peers []domain.Peer) []string {
	ids := make([]string, len(peers))
	for i, p := range peers {
		ids[i] = string(p.Identifier)
	}
	return ids
}

// EnsurePeerMetricsFor ensures that metrics exist for a specific peer by ID.
func (c *StatisticsCollector) EnsurePeerMetricsFor(ctx context.Context, peerID domain.PeerIdentifier) {
	peer, err := c.db.GetPeer(ctx, peerID)
	if err != nil || peer == nil {
		slog.Warn("failed to fetch peer for metrics ensure", "peerID", peerID, "error", err)
		return
	}
	c.RLock()
	_, exists := c.metricsLabels[peer.Identifier]
	c.RUnlock()
	if exists {
		return
	}
	var displayName, endpoint, allowedIPsStr string
	displayName = peer.DisplayName
	endpoint = peer.Endpoint.Value
	allowedIPsStr = peer.AllowedIPsStr.Value
       convertedPeer := domain.Peer{
	       Identifier:          peer.Identifier,
	       Endpoint:            domain.ConfigOption[string]{Value: endpoint},
	       InterfaceIdentifier: peer.InterfaceIdentifier,
	       DisplayName:         displayName,
	       AllowedIPsStr:       domain.ConfigOption[string]{Value: allowedIPsStr},
       }
       // Keep original peer data for ping functionality
       convertedPeer.Interface = peer.Interface
       c.peerCache[peer.Identifier] = &convertedPeer

       // Initialize ping success as false
       c.peerPingSuccess[peer.Identifier] = false

	// Try to get status from DB
	status, err := c.db.GetPeer(ctx, peer.Identifier)
	if err == nil && status != nil {
		if ps, ok := any(status).(*domain.PeerStatus); ok {
			c.updatePeerMetrics(ctx, *ps)
		} else {
			c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
		}
	} else {
		c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
	}
}

// NewStatisticsCollector creates and initializes a new StatisticsCollector with the provided dependencies.
// It sets up the collector with database connection, event bus, WireGuard controller manager,
// and metrics server, then connects to the message bus for real-time event handling.
func NewStatisticsCollector(
	cfg *config.Config,
	bus StatisticsEventBus,
	db StatisticsDatabaseRepo,
	wg *ControllerManager,
	ms StatisticsMetricsServer,
) (*StatisticsCollector, error) {
	c := &StatisticsCollector{
		cfg:           cfg,
		bus:           bus,
		db:            db,
		wg:            wg,
		ms:            ms,
		metricsLabels: make(map[domain.PeerIdentifier][]string),
		peerCache:     make(map[domain.PeerIdentifier]*domain.Peer),
		peerPingSuccess: make(map[domain.PeerIdentifier]bool),
	}
	c.connectToMessageBus()
	return c, nil
}

// EnsurePeerMetrics ensures that metrics exist for all peers in the database.
// This is typically called after fanout/sync operations. On startup, peers are
// initialized with empty address fields which get populated after successful ping checks.
func (c *StatisticsCollector) EnsurePeerMetrics(ctx context.Context) {
    dbPeers, err := c.db.GetAllPeers(ctx)
    if err != nil {
        slog.Warn("failed to fetch all peers for metrics ensure", "error", err)
        return
    }
    slog.Debug("[EnsurePeerMetrics] peers to process", "count", len(dbPeers), "ids", getPeerIDs(dbPeers))
    for _, peer := range dbPeers {
        c.RLock()
        _, exists := c.metricsLabels[peer.Identifier]
        c.RUnlock()
        if exists {
            slog.Debug("[EnsurePeerMetrics] metrics already exist, skipping", "peerID", peer.Identifier)
            continue
        }
        
        // Metrics don't exist, create them
        peerModel, err := c.db.GetPeer(ctx, peer.Identifier)
        var displayName string
        var endpoint string
        var allowedIPsStr string
        if err == nil && peerModel != nil {
            displayName = peerModel.DisplayName
            endpoint = peerModel.Endpoint.Value
            allowedIPsStr = peerModel.AllowedIPsStr.Value
        } else {
            endpoint = peer.Endpoint.Value
            allowedIPsStr = peer.AllowedIPsStr.Value
        }
        convertedPeer := domain.Peer{
            Identifier:          peer.Identifier,
            Endpoint:            domain.ConfigOption[string]{Value: endpoint},
            InterfaceIdentifier: peer.InterfaceIdentifier,
            DisplayName:         displayName,
            AllowedIPsStr:       domain.ConfigOption[string]{Value: allowedIPsStr},
        }
        // Keep original peer interface config for ping functionality
        convertedPeer.Interface = peer.Interface
        
        c.peerCache[peer.Identifier] = &convertedPeer

        slog.Debug("[EnsurePeerMetrics] peer cache initialized", "peerID", peer.Identifier, "allowedIPsStr", allowedIPsStr)

        // Initialize ping success as false (address will be hidden until first successful ping)
        c.peerPingSuccess[peer.Identifier] = false

        status, err := c.db.GetPeer(ctx, peer.Identifier)
        if err == nil && status != nil {
            if ps, ok := any(status).(*domain.PeerStatus); ok {
                slog.Debug("[EnsurePeerMetrics] updating metrics for peer", "peerID", peer.Identifier)
                c.updatePeerMetrics(ctx, *ps)
            } else {
                slog.Debug("[EnsurePeerMetrics] updating metrics for peer (default status)", "peerID", peer.Identifier)
                c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
            }
        } else {
            slog.Debug("[EnsurePeerMetrics] updating metrics for peer (not found)", "peerID", peer.Identifier)
            c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
        }
    }
}

// StartBackgroundJobs starts all background jobs for the statistics collector.
// This method is non-blocking and returns immediately after starting all workers.
func (c *StatisticsCollector) StartBackgroundJobs(ctx context.Context) {
       // Initial population of peer metrics
       c.EnsurePeerMetrics(ctx)

       // Explicitly update interface metrics for all interfaces at startup
       interfaces, err := c.db.GetAllInterfaces(ctx)
       if err == nil {
	       for _, iface := range interfaces {
		       // Create a default status with zeroed values
		       status := domain.InterfaceStatus{
			       InterfaceId: iface.Identifier,
			       BytesReceived: 0,
			       BytesTransmitted: 0,
		       }
		       c.updateInterfaceMetrics(status)
	       }
       }

       c.startPingWorkers(ctx)
       c.startInterfaceDataFetcher(ctx)
       c.startPeerDataFetcher(ctx)

       go func() {
	       <-ctx.Done()
       }()
}

// startInterfaceDataFetcher starts the interface data collection worker if enabled in config.
func (c *StatisticsCollector) startInterfaceDataFetcher(ctx context.Context) {
	if !c.cfg.Statistics.CollectInterfaceData {
		return
	}

	go c.collectInterfaceData(ctx)

	slog.Debug("started interface data fetcher")
}

// collectInterfaceData periodically collects and updates interface statistics.
func (c *StatisticsCollector) collectInterfaceData(ctx context.Context) {
	// Start ticker
	ticker := time.NewTicker(c.cfg.Statistics.DataCollectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // program stopped
		case <-ticker.C:
			interfaces, err := c.db.GetAllInterfaces(ctx)
			if err != nil {
				slog.Warn("failed to fetch all interfaces for data collection", "error", err)
				continue
			}

			for _, in := range interfaces {
				physicalInterface, err := c.wg.GetController(in).GetInterface(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to load physical interface for data collection", "interface", in.Identifier,
						"error", err)
					continue
				}
				err = c.db.UpdateInterfaceStatus(ctx, in.Identifier,
					func(i *domain.InterfaceStatus) (*domain.InterfaceStatus, error) {
						i.UpdatedAt = time.Now()
						i.BytesReceived = physicalInterface.BytesDownload
						i.BytesTransmitted = physicalInterface.BytesUpload

						// Update prometheus metrics
						go c.updateInterfaceMetrics(*i)

						return i, nil
					})
				if err != nil {
					slog.Warn("failed to update interface status", "interface", in.Identifier, "error", err)
				}
				slog.Debug("updated interface status", "interface", in.Identifier)
			}
		}
	}
}

// startPeerDataFetcher starts the peer data collection worker if enabled in config.
func (c *StatisticsCollector) startPeerDataFetcher(ctx context.Context) {
	if !c.cfg.Statistics.CollectPeerData {
		return
	}

	go c.collectPeerData(ctx)

	slog.Debug("started peer data fetcher")
}

// collectPeerData periodically collects and updates peer statistics.
func (c *StatisticsCollector) collectPeerData(ctx context.Context) {
	// Start ticker
	ticker := time.NewTicker(c.cfg.Statistics.DataCollectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // program stopped
		case <-ticker.C:
			interfaces, err := c.db.GetAllInterfaces(ctx)
			if err != nil {
				slog.Warn("failed to fetch all interfaces for peer data collection", "error", err)
				continue
			}

			for _, in := range interfaces {
				peers, err := c.wg.GetController(in).GetPeers(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to fetch peers for data collection", "interface", in.Identifier, "error", err)
					continue
				}
					for _, peer := range peers {
						var connectionStateChanged bool
						var newPeerStatus domain.PeerStatus
						err = c.db.UpdatePeerStatus(ctx, peer.Identifier,
							func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
								wasConnected := p.IsConnected

								var lastHandshake *time.Time
								if !peer.LastHandshake.IsZero() {
									lastHandshake = &peer.LastHandshake
								}

								// calculate if session was restarted
								p.UpdatedAt = time.Now()
								p.LastSessionStart = getSessionStartTime(*p, peer.BytesUpload, peer.BytesDownload,
									lastHandshake)
								p.BytesReceived = peer.BytesUpload      // store bytes that where uploaded from the peer and received by the server
								p.BytesTransmitted = peer.BytesDownload // store bytes that where received from the peer and sent by the server
								p.Endpoint = peer.Endpoint
								p.LastHandshake = lastHandshake
								p.CalcConnected()

								if wasConnected != p.IsConnected {
									slog.Debug("peer connection state changed", "peer", peer.Identifier, "connected", p.IsConnected)
									connectionStateChanged = true
									newPeerStatus = *p // store new status for event publishing
								}

								// Update prometheus metrics
								go c.updatePeerMetrics(ctx, *p)

								// Update the cache when peer data changes - PRESERVE existing cache data
								c.Lock()
								existingPeer, exists := c.peerCache[peer.Identifier]
								if exists {
									// Preserve existing peer data and just update dynamic fields
									existingPeer.Endpoint = domain.ConfigOption[string]{Value: peer.Endpoint}
									// Don't overwrite AllowedIPsStr or ping success status
									slog.Debug("[collectPeerData] preserving existing peer cache", "peerID", peer.Identifier, "allowedIPsStr", existingPeer.AllowedIPsStr.GetValue())
								} else {
									// Get DisplayName from DB model
									peerModel, err := c.db.GetPeer(ctx, peer.Identifier)
									var displayName string
									var allowedIPsStr string
									if err == nil && peerModel != nil {
										displayName = peerModel.DisplayName
										allowedIPsStr = peerModel.AllowedIPsStr.Value
									} else {
										// Convert AllowedIPs from []domain.Cidr to []string as fallback
										allowedIPs := make([]string, len(peer.AllowedIPs))
										for i, cidr := range peer.AllowedIPs {
											allowedIPs[i] = cidr.String()
										}
										allowedIPsStr = strings.Join(allowedIPs, ",")
									}

									convertedPeer := domain.Peer{
										Identifier:          peer.Identifier,
										Endpoint:            domain.ConfigOption[string]{Value: peer.Endpoint},
										InterfaceIdentifier: in.Identifier,
										DisplayName:         displayName,
										AllowedIPsStr:       domain.ConfigOption[string]{Value: allowedIPsStr},
									}

									c.peerCache[peer.Identifier] = &convertedPeer
									// Initialize ping success as false for new peers
									c.peerPingSuccess[peer.Identifier] = false
									slog.Debug("[collectPeerData] created new peer cache entry", "peerID", peer.Identifier, "allowedIPsStr", allowedIPsStr)
								}
								c.Unlock()

								return p, nil
							})
					if err != nil {
						slog.Warn("failed to update peer status", "peer", peer.Identifier, "error", err)
					} else {
						slog.Debug("updated peer status", "peer", peer.Identifier)
					}

					if connectionStateChanged {
						peerModel, err := c.db.GetPeer(ctx, peer.Identifier)
						if err != nil {
							slog.Error("failed to fetch peer for data collection", "peer", peer.Identifier, "error",
								err)
							continue
						}
						// publish event if connection state changed
						c.bus.Publish(app.TopicPeerStateChanged, newPeerStatus, *peerModel)
					}
				}
			}
		}
	}
}

// getSessionStartTime determines the session start time based on handshake and traffic patterns.
func getSessionStartTime(
oldStats domain.PeerStatus,
newReceived, newTransmitted uint64,
latestHandshake *time.Time,
) *time.Time {
	if latestHandshake == nil {
		return nil // currently not connected
	}

	oldestHandshakeTime := time.Now().Add(-2 * time.Minute) // if a handshake is older than 2 minutes, the peer is no longer connected
	switch {
	// old session was never initiated
	case oldStats.BytesReceived == 0 && oldStats.BytesTransmitted == 0 && (newReceived > 0 || newTransmitted > 0):
		return latestHandshake
	// session never received bytes -> first receive
	case oldStats.BytesReceived == 0 && newReceived > 0 && (oldStats.LastHandshake == nil || oldStats.LastHandshake.Before(oldestHandshakeTime)):
		return latestHandshake
	// session never transmitted bytes -> first transmit
	case oldStats.BytesTransmitted == 0 && newTransmitted > 0 && (oldStats.LastSessionStart == nil || oldStats.LastHandshake.Before(oldestHandshakeTime)):
		return latestHandshake
	// session restarted as newer send or transmit counts are lower
	case (newReceived != 0 && newReceived < oldStats.BytesReceived) || (newTransmitted != 0 && newTransmitted < oldStats.BytesTransmitted):
		return latestHandshake
	// session initiated (but some bytes were already transmitted
	case oldStats.LastSessionStart == nil && (newReceived > oldStats.BytesReceived || newTransmitted > oldStats.BytesTransmitted):
		return latestHandshake
	default:
		return oldStats.LastSessionStart
	}
}

// startPingWorkers starts the ping check workers if enabled in config.
func (c *StatisticsCollector) startPingWorkers(ctx context.Context) {
	if !c.cfg.Statistics.UsePingChecks {
		slog.Debug("ping checks disabled in config")
		return
	}

	if c.pingJobs != nil {
		slog.Debug("ping workers already started")
		return // already started
	}

	slog.Info("starting ping workers", "workers", c.cfg.Statistics.PingCheckWorkers, "interval", c.cfg.Statistics.PingCheckInterval)

	c.pingWaitGroup = sync.WaitGroup{}
	c.pingWaitGroup.Add(c.cfg.Statistics.PingCheckWorkers)
	c.pingJobs = make(chan pingJob, c.cfg.Statistics.PingCheckWorkers)

	// start workers
	for i := 0; i < c.cfg.Statistics.PingCheckWorkers; i++ {
		slog.Debug("starting ping worker", "workerID", i)
		go c.pingWorker(ctx)
	}

	// start cleanup goroutine
	go func() {
		c.pingWaitGroup.Wait()
		slog.Debug("all ping workers stopped")
	}()

	go c.enqueuePingChecks(ctx)

	slog.Debug("ping system started successfully")
}

// enqueuePingChecks periodically enqueues ping jobs for all peers.
func (c *StatisticsCollector) enqueuePingChecks(ctx context.Context) {
	// Start ticker
	ticker := time.NewTicker(c.cfg.Statistics.PingCheckInterval)
	defer ticker.Stop()
	defer close(c.pingJobs)
	
	slog.Debug("[enqueuePingChecks] started with interval", "interval", c.cfg.Statistics.PingCheckInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Debug("[enqueuePingChecks] context cancelled, stopping")
			return // program stopped
		case <-ticker.C:
			slog.Debug("[enqueuePingChecks] tick - fetching interfaces")
			interfaces, err := c.db.GetAllInterfaces(ctx)
			if err != nil {
				slog.Warn("failed to fetch all interfaces for ping checks", "error", err)
				continue
			}
			slog.Debug("[enqueuePingChecks] found interfaces", "count", len(interfaces))

			totalJobsEnqueued := 0
			for _, in := range interfaces {
				peers, err := c.db.GetInterfacePeers(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to fetch peers for ping checks", "interface", in.Identifier, "error", err)
					continue
				}
				slog.Debug("[enqueuePingChecks] found peers for interface", "interface", in.Identifier, "peers", len(peers))
				
				for _, peer := range peers {
					slog.Debug("[enqueuePingChecks] enqueuing ping job", "peerID", peer.Identifier, "interface", in.Identifier)
					
					select {
					case c.pingJobs <- pingJob{
						Peer:    peer,
						Backend: in.Backend,
					}:
						totalJobsEnqueued++
					case <-ctx.Done():
						slog.Debug("[enqueuePingChecks] context cancelled while enqueuing")
						return
					default:
						slog.Warn("[enqueuePingChecks] ping jobs channel full, dropping job", "peerID", peer.Identifier)
					}
				}
			}
			slog.Debug("[enqueuePingChecks] enqueued ping jobs", "total", totalJobsEnqueued)
		}
	}
}

// pingWorker processes ping jobs from the queue and updates peer status.
// After a successful ping, the address field in peerCache is populated and metrics are updated.
func (c *StatisticsCollector) pingWorker(ctx context.Context) {
    defer c.pingWaitGroup.Done()
    slog.Debug("[pingWorker] worker started")
    
    for job := range c.pingJobs {
        peer := job.Peer
        backend := job.Backend
        
        slog.Debug("[pingWorker] processing ping job", "peerID", peer.Identifier, "backend", backend)

        var connectionStateChanged bool
        var newPeerStatus domain.PeerStatus

        peerPingable := c.isPeerPingable(ctx, backend, peer)
        slog.Debug("[pingWorker] peer ping check completed", "peer", peer.Identifier, "pingable", peerPingable)

        now := time.Now()
        err := c.db.UpdatePeerStatus(ctx, peer.Identifier,
            func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
                wasConnected := p.IsConnected

                if peerPingable {
                    p.IsPingable = true
                    p.LastPing = &now
                    
                    // Mark ping as successful and store the pinged address
                    c.Lock()
                    c.peerPingSuccess[peer.Identifier] = true
                    pingAddr := getPeerPingAddress(peer)
                    slog.Info("[pingWorker] peer ping successful - address will now be shown in metrics", "peerID", peer.Identifier, "address", pingAddr)
                    c.Unlock()
                } else {
                    p.IsPingable = false
                    p.LastPing = nil
                    slog.Debug("[pingWorker] ping failed", "peerID", peer.Identifier)
                }
                p.UpdatedAt = time.Now()
                p.CalcConnected()

                if wasConnected != p.IsConnected {
                    connectionStateChanged = true
                    newPeerStatus = *p // store new status for event publishing
                }

                // Update prometheus metrics after address update
                go c.updatePeerMetrics(ctx, *p)

                return p, nil
            })
        if err != nil {
            slog.Warn("failed to update peer ping status", "peer", peer.Identifier, "error", err)
        } else {
            slog.Debug("updated peer ping status", "peer", peer.Identifier)
        }

        if connectionStateChanged {
            // publish event if connection state changed
            c.bus.Publish(app.TopicPeerStateChanged, newPeerStatus, peer)
        }
    }
    slog.Debug("[pingWorker] worker stopped")
}

// isPeerPingable checks if a peer is reachable via ping.
func (c *StatisticsCollector) isPeerPingable(
ctx context.Context,
backend domain.InterfaceBackend,
peer domain.Peer,
) bool {
	if !c.cfg.Statistics.UsePingChecks {
		slog.Debug("[isPeerPingable] ping checks disabled", "peerID", peer.Identifier)
		return false
	}

	checkAddr := getPeerPingAddress(peer)
	slog.Debug("[isPeerPingable] checking address", "peerID", peer.Identifier, "checkAddr", checkAddr, "allowedIPsStr", peer.AllowedIPsStr.GetValue())
	if checkAddr == "" {
		slog.Debug("[isPeerPingable] no address to ping", "peerID", peer.Identifier)
		return false
	}

	slog.Debug("[isPeerPingable] attempting ping", "peerID", peer.Identifier, "address", checkAddr, "backend", backend)
	stats, err := c.wg.GetControllerByName(backend).PingAddresses(ctx, checkAddr)
	if err != nil {
		slog.Debug("failed to ping peer", "peer", peer.Identifier, "address", checkAddr, "error", err)
		return false
	}

	result := stats.IsPingable()
	slog.Debug("[isPeerPingable] ping result", "peerID", peer.Identifier, "address", checkAddr, "pingable", result)
	return result
}

// updateInterfaceMetrics updates interface metrics in Prometheus.
func (c *StatisticsCollector) updateInterfaceMetrics(status domain.InterfaceStatus) {
	c.ms.UpdateInterfaceMetrics(status)
}

// updatePeerMetrics updates peer metrics in Prometheus with current status.
func (c *StatisticsCollector) updatePeerMetrics(ctx context.Context, status domain.PeerStatus) {
    // Check if peer data is in the cache first for performance
    peer, exists := c.peerCache[status.PeerId]
    if !exists {
        // Fallback to database like original implementation
        peerFromDB, err := c.db.GetPeer(ctx, status.PeerId)
        if err != nil {
            slog.Warn("failed to fetch peer data for metrics", "peer", status.PeerId, "error", err)
            return
        }
        peer = peerFromDB
    }

    // Get checkAddr - show empty until first successful ping (only for cached peers)
    var checkAddr string
    if exists {
        c.RLock()
        hasPingSuccess := c.peerPingSuccess[status.PeerId]
        c.RUnlock()
        
        if hasPingSuccess {
            checkAddr = getPeerPingAddress(*peer)
        } else {
            checkAddr = "" // empty until first successful ping
        }
    } else {
        // For non-cached peers (direct from DB), use normal address
        checkAddr = peer.CheckAliveAddress()
    }
    
    slog.Debug("updatePeerMetrics", "peerID", status.PeerId, "checkAddr", checkAddr, "fromCache", exists)
    
    // Store labels for cached peers only
    if exists {
        labels := []string{
            string(peer.InterfaceIdentifier),
            string(status.PeerId),
            peer.DisplayName,
            checkAddr,
        }

        c.Lock()
        c.metricsLabels[status.PeerId] = labels
        c.Unlock()
    }

    c.ms.UpdatePeerMetrics(peer, status)
}

// RemovePeerMetrics removes peer metrics from Prometheus, handling cases where
// peer records may be missing from the database.
func (c *StatisticsCollector) RemovePeerMetrics(peerID domain.PeerIdentifier) error {
	if c.ms != nil {
		// Attempt to fetch peer from the database
		peer, err := c.db.GetPeer(context.Background(), peerID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				slog.Warn("Peer not found in database for metrics removal", "peerID", peerID)

				// Attempt to use cached labels for removal
				c.Lock()
				labels, exists := c.metricsLabels[peerID]
				c.Unlock()
				if exists {
					slog.Debug("Removing metrics using cached labels", "peerID", peerID, "labels", labels)
					c.ms.RemovePeerMetrics(&domain.Peer{Identifier: peerID})
					c.Lock()
					delete(c.metricsLabels, peerID)
					c.Unlock()
				} else {
					slog.Warn("No cached labels found for peer metrics removal", "peerID", peerID)
				}
				return nil
			}
			return fmt.Errorf("failed to fetch peer for metrics removal: %w", err)
		}


		// Retrieve labels from the map
		c.Lock()
		labels, exists := c.metricsLabels[peerID]
		if exists {
			slog.Debug("Removing metrics for peer", "peerID", peerID, "labels", labels)
			delete(c.metricsLabels, peerID) // Remove labels from the map
		}
		c.Unlock()

		c.ms.RemovePeerMetrics(peer)
	}
	return nil
}

// connectToMessageBus subscribes to message bus events for real-time metrics updates.
func (c *StatisticsCollector) connectToMessageBus() {
	_ = c.bus.Subscribe(app.TopicPeerIdentifierUpdated, c.handlePeerIdentifierChangeEvent)
	// Subscribe to peer creation event for instant metrics
	_ = c.bus.Subscribe(app.TopicPeerCreated, c.handlePeerCreatedEvent)
	// Subscribe to peer update event for instant metrics (sync/fanout)
	_ = c.bus.Subscribe(app.TopicPeerUpdated, c.handlePeerCreatedEvent)

}

// handlePeerCreatedEvent handles peer creation events and instantly updates metrics for new peers.
func (c *StatisticsCollector) handlePeerCreatedEvent(peer domain.Peer) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	// Update cache and metrics for new peer
	peerModel, err := c.db.GetPeer(ctx, peer.Identifier)
	var displayName string
	var endpoint string
	var allowedIPsStr string
	if err == nil && peerModel != nil {
		displayName = peerModel.DisplayName
		endpoint = peerModel.Endpoint.Value
		allowedIPsStr = peerModel.AllowedIPsStr.Value
	} else {
		endpoint = peer.Endpoint.Value
		allowedIPsStr = peer.AllowedIPsStr.Value
	}
       convertedPeer := domain.Peer{
	       Identifier:          peer.Identifier,
	       Endpoint:            domain.ConfigOption[string]{Value: endpoint},
	       InterfaceIdentifier: peer.InterfaceIdentifier,
	       DisplayName:         displayName,
	       AllowedIPsStr:       domain.ConfigOption[string]{Value: allowedIPsStr},
       }
       // Keep original peer interface config for ping functionality  
       convertedPeer.Interface = peer.Interface
       c.peerCache[peer.Identifier] = &convertedPeer

       // Initialize ping success as false
       c.peerPingSuccess[peer.Identifier] = false

	// Try to get status from DB
	status, err := c.db.GetPeer(ctx, peer.Identifier)
	if err == nil && status != nil {
		if ps, ok := any(status).(*domain.PeerStatus); ok {
			c.updatePeerMetrics(ctx, *ps)
		} else {
			c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
		}
	} else {
		c.updatePeerMetrics(ctx, domain.PeerStatus{PeerId: peer.Identifier})
	}
}

// handlePeerIdentifierChangeEvent handles peer identifier change events and cleans up old status data.
func (c *StatisticsCollector) handlePeerIdentifierChangeEvent(oldIdentifier, newIdentifier domain.PeerIdentifier) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())

	// remove potential left-over status data
	err := c.db.DeletePeerStatus(ctx, oldIdentifier)
	if err != nil {
		slog.Error("failed to delete old peer status for migrated peer", "oldIdentifier", oldIdentifier,
			"newIdentifier", newIdentifier, "error", err)
	}
}
