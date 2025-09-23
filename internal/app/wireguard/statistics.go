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

type StatisticsMetricsServer interface {
	UpdateInterfaceMetrics(status domain.InterfaceStatus)
	UpdatePeerMetrics(peer *domain.Peer, status domain.PeerStatus)
	RemovePeerMetrics(peer *domain.Peer)
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

	// Map to store metrics labels by Peer ID
	metricsLabels map[domain.PeerIdentifier][]string

	// Add a mutex to protect the metricsLabels map
	sync.RWMutex

	// Add a cache to store peer data
	peerCache map[domain.PeerIdentifier]*domain.Peer
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
		cfg:           cfg,
		bus:           bus,
		db:            db,
		wg:            wg,
		ms:            ms,
		metricsLabels: make(map[domain.PeerIdentifier][]string), // Initialize the map
		peerCache:     make(map[domain.PeerIdentifier]*domain.Peer),
	}

	c.connectToMessageBus()

	return c, nil
}

// StartBackgroundJobs starts the background jobs for the statistics collector.
// This method is non-blocking and returns immediately.
func (c *StatisticsCollector) StartBackgroundJobs(ctx context.Context) {
		c.startPingWorkers(ctx)
		c.startInterfaceDataFetcher(ctx)
		c.startPeerDataFetcher(ctx)

		go func() {
			 <-ctx.Done()
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

							// Update the cache when peer data changes
							convertedPeer := domain.Peer{
								Identifier:  peer.Identifier,
								Endpoint:   domain.ConfigOption[string]{Value: peer.Endpoint},
							}

							// Convert AllowedIPs from []domain.Cidr to []string
							// Use the String() method of Cidr to convert to string
							allowedIPs := make([]string, len(peer.AllowedIPs))
							for i, cidr := range peer.AllowedIPs {
								allowedIPs[i] = cidr.String()
							}
							convertedPeer.AllowedIPsStr = domain.ConfigOption[string]{Value: strings.Join(allowedIPs, ",")}

							c.peerCache[peer.Identifier] = &convertedPeer

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

	go c.enqueuePingChecks(ctx)

	slog.Debug("started ping checks")
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
		err := c.db.UpdatePeerStatus(ctx, peer.Identifier,
			func(p *domain.PeerStatus) (*domain.PeerStatus, error) {
				wasConnected := p.IsConnected

				if peerPingable {
					p.IsPingable = true
					p.LastPing = &now
				} else {
					p.IsPingable = false
					p.LastPing = nil
				}
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

		if connectionStateChanged {
			// publish event if connection state changed
			c.bus.Publish(app.TopicPeerStateChanged, newPeerStatus, peer)
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
	// Check if peer data is in the cache
	peer, exists := c.peerCache[status.PeerId]
	if !exists {
		slog.Warn("peer data not found in cache", "peer", status.PeerId)
		return
	}

	labels := []string{
		string(peer.InterfaceIdentifier),
		peer.Interface.AddressStr(),
		string(status.PeerId),
		peer.DisplayName,
	}

	// Validate labels for empty values
	if peer.Interface.AddressStr() == "" {
		slog.Warn("Empty address in peer labels", "peerID", status.PeerId)
	}
	if peer.DisplayName == "" {
		slog.Warn("Empty display name in peer labels", "peerID", status.PeerId)
	}

	// Lock the mutex before writing to the map
	c.Lock()
	defer c.Unlock()

	// Store labels in the map
	c.metricsLabels[status.PeerId] = labels

	c.ms.UpdatePeerMetrics(peer, status)
}

// Update RemovePeerMetrics to handle missing peer records
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

func (c *StatisticsCollector) connectToMessageBus() {
	_ = c.bus.Subscribe(app.TopicPeerIdentifierUpdated, c.handlePeerIdentifierChangeEvent)
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
