package wireguard

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type StatisticsDatabaseRepo interface {
	GetAllInterfaces(ctx context.Context) ([]domain.Interface, error)
	GetInterfacePeers(ctx context.Context, id domain.InterfaceIdentifier) ([]domain.Peer, error)
	GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error)
	GetAllPeers(ctx context.Context) ([]domain.Peer, error)
	GetAllPeerStatuses(ctx context.Context) ([]domain.PeerStatus, error)
	UpdatePeerStatus(
		ctx context.Context,
		id domain.PeerIdentifier,
		updateFunc func(in *domain.PeerStatus) (*domain.PeerStatus, error),
	) error
	// ClaimPeerStatus claims ownership of a peer status for this node
	// Sets the OwnerNodeId and updates the peer status
	// Returns error if ownership claim fails
	ClaimPeerStatus(
		ctx context.Context,
		id domain.PeerIdentifier,
		ownerNodeId string,
		updateFunc func(in *domain.PeerStatus) (*domain.PeerStatus, error),
	) error
	BatchUpdatePeerStatuses(
		ctx context.Context,
		updates map[domain.PeerIdentifier]func(in *domain.PeerStatus) (*domain.PeerStatus, error),
	) error
	UpdateInterfaceStatus(
		ctx context.Context,
		id domain.InterfaceIdentifier,
		updateFunc func(in *domain.InterfaceStatus) (*domain.InterfaceStatus, error),
	) error
	DeletePeerStatus(ctx context.Context, id domain.PeerIdentifier) error
}

type StatisticsMetricsServer interface {
	UpdateInterfaceMetrics(status domain.InterfaceStatus)
	UpdatePeerMetrics(peer *domain.Peer, status domain.PeerStatus)
	RemovePeerMetrics(peer *domain.Peer)
	RemovePeerMetricsByID(peerId string)
}

type StatisticsEventBus interface {
	// Subscribe subscribes to a topic
	Subscribe(topic string, fn interface{}) error
	// Publish sends a message to the message bus.
	Publish(topic string, args ...any)
}

type pingJob struct {
	Peer    domain.Peer
	Backend domain.InterfaceBackend
}

type StatisticsCollector struct {
	cfg *config.Config
	bus StatisticsEventBus

	pingWaitGroup sync.WaitGroup
	pingJobs      chan pingJob

	db StatisticsDatabaseRepo
	wg *ControllerManager
	ms StatisticsMetricsServer

	peerChangeEvent chan domain.PeerIdentifier
}

// NewStatisticsCollector creates a new statistics collector.
func NewStatisticsCollector(
	cfg *config.Config,
	bus StatisticsEventBus,
	db StatisticsDatabaseRepo,
	wg *ControllerManager,
	ms StatisticsMetricsServer,
) (*StatisticsCollector, error) {
	c := &StatisticsCollector{
		cfg: cfg,
		bus: bus,

		db: db,
		wg: wg,
		ms: ms,
	}

	c.connectToMessageBus()

	return c, nil
}

// StartBackgroundJobs starts the background jobs for the statistics collector.
// This method is non-blocking and returns immediately after launching background goroutines.
// Background jobs are delayed by 10 seconds to allow database connection pool to stabilize
// and avoid connection storms during node startup in multi-node clusters.
func (c *StatisticsCollector) StartBackgroundJobs(ctx context.Context) {
	// Start background job launcher with delay to allow connection pool stabilization
	go func() {
		// Wait 10 seconds before starting background jobs to allow:
		// 1. Initial database connections to establish
		// 2. Interface state restoration to complete
		// 3. Connection pool to settle
		// 4. Other services to start up
		select {
		case <-ctx.Done():
			return // context cancelled before delay complete
		case <-time.After(10 * time.Second):
		}

		slog.Info("starting background statistics jobs after startup delay")
		c.startPingWorkers(ctx)
		c.startInterfaceDataFetcher(ctx)
		c.startPeerDataFetcher(ctx)
	}()
}

func (c *StatisticsCollector) startInterfaceDataFetcher(ctx context.Context) {
	if !c.cfg.Statistics.CollectInterfaceData {
		return
	}

	go c.collectInterfaceData(ctx)

	slog.Debug("started interface data fetcher")
}

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

func (c *StatisticsCollector) startPeerDataFetcher(ctx context.Context) {
	if !c.cfg.Statistics.CollectPeerData {
		return
	}

	go c.collectPeerData(ctx)

	slog.Debug("started peer data fetcher")
}

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
				// Get peers from WireGuard (physical interface)
				wireguardPeers, err := c.wg.GetController(in).GetPeers(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to fetch peers for data collection", "interface", in.Identifier, "error", err)
					continue
				}

				// Create map of existing WireGuard peers for quick lookup
				wireguardPeerMap := make(map[domain.PeerIdentifier]bool)
				for _, peer := range wireguardPeers {
					wireguardPeerMap[peer.Identifier] = true
				}

				// Get all peers from database for this interface
				dbPeers, err := c.db.GetInterfacePeers(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to fetch database peers for cleanup", "interface", in.Identifier, "error", err)
				} else {
					// Create map of database peers for quick lookup
					dbPeerMap := make(map[domain.PeerIdentifier]bool)
					for _, dbPeer := range dbPeers {
						dbPeerMap[dbPeer.Identifier] = true
					}

					// Clean up statuses for peers that no longer exist in WireGuard
					for _, dbPeer := range dbPeers {
						if !wireguardPeerMap[dbPeer.Identifier] {
							// Peer exists in DB but not in WireGuard - mark as disconnected
							// Do NOT delete metrics, just set to 0 (peer still exists in DB)
							err := c.db.UpdatePeerStatus(ctx, dbPeer.Identifier,
								func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
									if p.IsConnected || p.IsPingable {
										slog.Debug("peer not found in wireguard, marking as disconnected",
											"peer", dbPeer.Identifier)
										p.IsConnected = false
										p.IsPingable = false
										p.LastHandshake = nil
										p.UpdatedAt = time.Now()

										// Update metrics to show disconnected state (metrics = 0)
										go c.updatePeerMetrics(ctx, *p)
									}
									return p, nil
								})
							if err != nil {
								slog.Warn("failed to update disconnected peer status", "peer", dbPeer.Identifier, "error", err)
							}
						}
					}

					// Also clean up metrics for WireGuard peers that no longer exist in DB
					// This handles the case where peer was deleted but metrics remain
					for _, wgPeer := range wireguardPeers {
						if !dbPeerMap[wgPeer.Identifier] {
							slog.Debug("peer found in wireguard but not in database, removing metrics",
								"peer", wgPeer.Identifier)
							c.ms.RemovePeerMetricsByID(string(wgPeer.Identifier))
						}
					}
				}

				// Process WireGuard peers
				for _, peer := range wireguardPeers {
					var connectionStateChanged bool
					var newPeerStatus domain.PeerStatus

					// If peer has a recent handshake, claim ownership for this node
					isConnected := !peer.LastHandshake.IsZero() && peer.LastHandshake.After(time.Now().Add(-2*time.Minute))

					var err error
					if isConnected {
						// Claim ownership since peer is connected to this node
						err = c.db.ClaimPeerStatus(ctx, peer.Identifier, c.cfg.Core.ClusterNodeId,
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

								return p, nil
							})
						if err != nil {
							slog.Warn("failed to claim and update peer status", "peer", peer.Identifier, "node", c.cfg.Core.ClusterNodeId, "error", err)
						} else {
							slog.Debug("claimed and updated peer status", "peer", peer.Identifier, "node", c.cfg.Core.ClusterNodeId)
						}
					} else {
						// Peer is not connected to this node, just update (don't claim)
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

								return p, nil
							})
						if err != nil {
							slog.Warn("failed to update peer status", "peer", peer.Identifier, "error", err)
						} else {
							slog.Debug("updated peer status", "peer", peer.Identifier)
						}
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

					// Spread out peer updates across time to avoid connection pool exhaustion
					// 100ms per peer spreads all peers across ~29 seconds per collection cycle
					// This ensures no burst of concurrent database queries
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
		}
	}
}

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

func (c *StatisticsCollector) startPingWorkers(ctx context.Context) {
	if !c.cfg.Statistics.UsePingChecks {
		return
	}

	if c.pingJobs != nil {
		return // already started
	}

	c.pingWaitGroup = sync.WaitGroup{}
	c.pingWaitGroup.Add(c.cfg.Statistics.PingCheckWorkers)
	c.pingJobs = make(chan pingJob, c.cfg.Statistics.PingCheckWorkers)

	// start workers
	for i := 0; i < c.cfg.Statistics.PingCheckWorkers; i++ {
		go c.pingWorker(ctx)
	}

	// start cleanup goroutine
	go func() {
		c.pingWaitGroup.Wait()

		slog.Debug("stopped ping checks")
	}()

	// Start ping checks with delay to avoid overwhelming database on startup
	// This gives the system time to recover from initial synchronization stress
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			slog.Info("starting ping checks after 30s startup delay")
			c.enqueuePingChecks(ctx)
		}
	}()

	slog.Debug("scheduled ping checks to start after 30 seconds")
}

func (c *StatisticsCollector) enqueuePingChecks(ctx context.Context) {
	// Start ticker
	ticker := time.NewTicker(c.cfg.Statistics.PingCheckInterval)
	defer ticker.Stop()
	defer close(c.pingJobs)

	for {
		select {
		case <-ctx.Done():
			return // program stopped
		case <-ticker.C:
			interfaces, err := c.db.GetAllInterfaces(ctx)
			if err != nil {
				slog.Warn("failed to fetch all interfaces for ping checks", "error", err)
				continue
			}

			for _, in := range interfaces {
				peers, err := c.db.GetInterfacePeers(ctx, in.Identifier)
				if err != nil {
					slog.Warn("failed to fetch peers for ping checks", "interface", in.Identifier, "error", err)
					continue
				}
				for _, peer := range peers {
					c.pingJobs <- pingJob{
						Peer:    peer,
						Backend: in.Backend,
					}
				}
			}
		}
	}
}

func (c *StatisticsCollector) pingWorker(ctx context.Context) {
	defer c.pingWaitGroup.Done()
	for job := range c.pingJobs {
		peer := job.Peer
		backend := job.Backend

		var connectionStateChanged bool
		var newPeerStatus domain.PeerStatus

		peerPingable := c.isPeerPingable(ctx, backend, peer)
		slog.Debug("peer ping check completed", "peer", peer.Identifier, "pingable", peerPingable)

		now := time.Now()

		// If peer is pingable/connected, claim ownership for this node
		// This ensures only the node where the peer is connected updates its status
		if peerPingable {
			err := c.db.ClaimPeerStatus(ctx, peer.Identifier, c.cfg.Core.ClusterNodeId,
				func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
					wasConnected := p.IsConnected

					p.IsPingable = true
					p.LastPing = &now
					p.UpdatedAt = time.Now()
					p.CalcConnected()

					if wasConnected != p.IsConnected {
						connectionStateChanged = true
						newPeerStatus = *p // store new status for event publishing
					}

					// Update prometheus metrics
					go c.updatePeerMetrics(ctx, *p)

					return p, nil
				})
			if err != nil {
				slog.Warn("failed to claim and update peer ping status", "peer", peer.Identifier, "node", c.cfg.Core.ClusterNodeId, "error", err)
			} else {
				slog.Debug("claimed and updated peer ping status", "peer", peer.Identifier, "node", c.cfg.Core.ClusterNodeId)
			}
		} else {
			// For unpingable peers, use regular update (doesn't claim ownership)
			err := c.db.UpdatePeerStatus(ctx, peer.Identifier,
				func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
					wasConnected := p.IsConnected

					p.IsPingable = false
					p.LastPing = nil
					p.UpdatedAt = time.Now()
					p.CalcConnected()

					if wasConnected != p.IsConnected {
						connectionStateChanged = true
						newPeerStatus = *p // store new status for event publishing
					}

					// Update prometheus metrics
					go c.updatePeerMetrics(ctx, *p)

					return p, nil
				})
			if err != nil {
				slog.Warn("failed to update peer ping status", "peer", peer.Identifier, "error", err)
			} else {
				slog.Debug("updated peer ping status", "peer", peer.Identifier)
			}
		}

		if connectionStateChanged {
			// publish event if connection state changed
			c.bus.Publish(app.TopicPeerStateChanged, newPeerStatus, peer)
		}

		// Add delay between updates to reduce concurrent database operations and connection pool exhaustion
		// With 3 workers and 283 peers, spreading updates over time prevents "Too many connections" errors
		// 100ms per peer × 3 workers = ~9.4 seconds total per cycle
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *StatisticsCollector) isPeerPingable(
	ctx context.Context,
	backend domain.InterfaceBackend,
	peer domain.Peer,
) bool {
	if !c.cfg.Statistics.UsePingChecks {
		return false
	}

	checkAddr := peer.CheckAliveAddress()
	if checkAddr == "" {
		return false
	}

	stats, err := c.wg.GetControllerByName(backend).PingAddresses(ctx, checkAddr)
	if err != nil {
		slog.Debug("failed to ping peer", "peer", peer.Identifier, "error", err)
		return false
	}

	return stats.IsPingable()
}

func (c *StatisticsCollector) updateInterfaceMetrics(status domain.InterfaceStatus) {
	c.ms.UpdateInterfaceMetrics(status)
}

func (c *StatisticsCollector) updatePeerMetrics(ctx context.Context, status domain.PeerStatus) {
	// Fetch peer data from the database
	peer, err := c.db.GetPeer(ctx, status.PeerId)
	if err != nil {
		// Peer not found in database - it's orphaned.
		// NOTE: We do NOT delete peer_status here.
		// The orphaned status will be cleaned up by CleanOrphanedStatuses on all cluster nodes.
		// This ensures proper cleanup across distributed systems.
		slog.Debug("skipping metrics update for orphaned peer", "peer", status.PeerId)
		return
	}
	c.ms.UpdatePeerMetrics(peer, status)
}

func (c *StatisticsCollector) connectToMessageBus() {
	_ = c.bus.Subscribe(app.TopicPeerIdentifierUpdated, c.handlePeerIdentifierChangeEvent)
	_ = c.bus.Subscribe(app.TopicPeerDeleted, c.handlePeerDeleteEvent)
}

func (c *StatisticsCollector) handlePeerIdentifierChangeEvent(oldIdentifier, newIdentifier domain.PeerIdentifier) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())

	// remove potential left-over status data
	err := c.db.DeletePeerStatus(ctx, oldIdentifier)
	if err != nil {
		slog.Error("failed to delete old peer status for migrated peer", "oldIdentifier", oldIdentifier,
			"newIdentifier", newIdentifier, "error", err)
	}
}

func (c *StatisticsCollector) handlePeerDeleteEvent(peer domain.Peer) {
	// NOTE: We do NOT delete peer_status from database here.
	// The peer_status will be cleaned up by CleanOrphanedStatuses on all cluster nodes.
	// This ensures that other nodes can detect orphaned statuses and clean up their metrics.

	// Remove metrics for the deleted peer on THIS node
	c.ms.RemovePeerMetrics(&peer)

	slog.Debug("cleaned up metrics for deleted peer on local node", "peerIdentifier", peer.Identifier)
}

// CleanOrphanedStatuses removes peer statuses and metrics for peers that no longer exist in the database.
// This is called after SyncAllPeersFromDB to ensure orphaned statuses are cleaned up.
// OPTIMIZATION: Only run this on the first cluster node to avoid 24x database load
// since all nodes would do the same work and just exhaust the connection pool
func (c *StatisticsCollector) CleanOrphanedStatuses(ctx context.Context) {
	// CRITICAL: Skip cleanup on non-primary nodes to prevent 24 nodes from hammering DB with identical queries
	// Only the node with ClusterNodeId of '1' or containing "node-1" should do this
	// This prevents N+1 query explosion (600+ queries per call) multiplied by 24 nodes = database collapse
	if !c.isPrimaryNode() {
		slog.Debug("CleanOrphanedStatuses: skipping on non-primary node", "node_id", c.cfg.Core.ClusterNodeId)
		return
	}

	slog.Info("CleanOrphanedStatuses: starting cleanup on primary node")

	// Get all peers from database
	dbPeers, err := c.db.GetAllPeers(ctx)
	if err != nil {
		slog.Warn("failed to fetch database peers for orphaned cleanup", "error", err)
		return
	}

	slog.Debug("CleanOrphanedStatuses: found DB peers", "count", len(dbPeers))

	// Create map of valid peer IDs
	validPeerMap := make(map[domain.PeerIdentifier]bool)
	for _, peer := range dbPeers {
		validPeerMap[peer.Identifier] = true
	}

	cleanedCount := 0

	// 1. Check peer_statuses table for orphaned records
	allStatuses, err := c.db.GetAllPeerStatuses(ctx)
	if err != nil {
		slog.Warn("failed to fetch peer statuses for orphaned cleanup", "error", err)
	} else {
		slog.Debug("CleanOrphanedStatuses: found peer statuses", "count", len(allStatuses))

		for _, status := range allStatuses {
			if !validPeerMap[status.PeerId] {
				slog.Info("found orphaned peer status, cleaning up", "peer", status.PeerId)

				// Delete orphaned status from database
				if err := c.db.DeletePeerStatus(ctx, status.PeerId); err != nil {
					slog.Warn("failed to delete orphaned peer status", "peer", status.PeerId, "error", err)
				}

				// Remove orphaned metrics from THIS node's registry
				c.ms.RemovePeerMetricsByID(string(status.PeerId))
				cleanedCount++
			}
		}
	}

	// 2. Also check WireGuard interfaces for peers that shouldn't be there
	// This catches cases where peer was removed but metrics still exist in memory
	interfaces, err := c.db.GetAllInterfaces(ctx)
	if err != nil {
		slog.Warn("failed to fetch interfaces for orphaned cleanup", "error", err)
	} else {
		for _, iface := range interfaces {
			wgPeers, err := c.wg.GetController(iface).GetPeers(ctx, iface.Identifier)
			if err != nil {
				slog.Debug("failed to fetch WireGuard peers for cleanup", "interface", iface.Identifier, "error", err)
				continue
			}

			slog.Debug("CleanOrphanedStatuses: checking WireGuard peers", "interface", iface.Identifier, "count", len(wgPeers))

			// Check each WireGuard peer - if it's not in DB, it's orphaned
			for _, wgPeer := range wgPeers {
				if !validPeerMap[wgPeer.Identifier] {
					slog.Info("found orphaned peer in WireGuard, cleaning up metrics", "peer", wgPeer.Identifier, "interface", iface.Identifier)

					// Remove orphaned metrics (status was already cleaned above or doesn't exist)
					c.ms.RemovePeerMetricsByID(string(wgPeer.Identifier))
					cleanedCount++
				}
			}
		}
	}

	if cleanedCount > 0 {
		slog.Info("cleaned up orphaned peer statuses and metrics", "count", cleanedCount)
	} else {
		slog.Debug("CleanOrphanedStatuses: no orphaned peers found")
	}
}

// isPrimaryNode returns true if this is the primary cleanup node
// Uses ClusterNodeId to determine primary (expected to end with "1")
func (c *StatisticsCollector) isPrimaryNode() bool {
	nodeId := c.cfg.Core.ClusterNodeId
	if nodeId == "" {
		return true // if no cluster ID, assume primary to ensure cleanup happens
	}
	// Node is marked as primary if its ID contains "-1" or ends with "1" suffix
	// This ensures only ONE designated node does the expensive cleanup
	return strings.Contains(nodeId, "-1")
}
