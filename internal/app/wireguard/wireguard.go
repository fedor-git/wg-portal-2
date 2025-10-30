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
	// no need to import wireguard here; StatisticsCollector is in the same package
)

// region dependencies

type InterfaceAndPeerDatabaseRepo interface {
	GetInterface(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, error)
	GetInterfaceAndPeers(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, []domain.Peer, error)
	GetPeersStats(ctx context.Context, ids ...domain.PeerIdentifier) ([]domain.PeerStatus, error)
	GetAllInterfaces(ctx context.Context) ([]domain.Interface, error)
	GetInterfaceIps(ctx context.Context) (map[domain.InterfaceIdentifier][]domain.Cidr, error)
	SaveInterface(
		ctx context.Context,
		id domain.InterfaceIdentifier,
		updateFunc func(in *domain.Interface) (*domain.Interface, error),
	) error
	DeleteInterface(ctx context.Context, id domain.InterfaceIdentifier) error
	GetInterfacePeers(ctx context.Context, id domain.InterfaceIdentifier) ([]domain.Peer, error)
	GetUserPeers(ctx context.Context, id domain.UserIdentifier) ([]domain.Peer, error)
	SavePeer(
		ctx context.Context,
		id domain.PeerIdentifier,
		updateFunc func(in *domain.Peer) (*domain.Peer, error),
	) error
	DeletePeer(ctx context.Context, id domain.PeerIdentifier) error
	GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error)
	GetUsedIpsPerSubnet(ctx context.Context, subnets []domain.Cidr) (map[domain.Cidr][]domain.Cidr, error)
	SyncAllPeersFromDB(ctx context.Context) (int, error) // Synchronize all peers from the database
}

type WgQuickController interface {
	ExecuteInterfaceHook(id domain.InterfaceIdentifier, hookCmd string) error
	SetDNS(id domain.InterfaceIdentifier, dnsStr, dnsSearchStr string) error
	UnsetDNS(id domain.InterfaceIdentifier) error
}

type EventBus interface {
	// Publish sends a message to the message bus.
	Publish(topic string, args ...any)
	// Subscribe subscribes to a topic
	Subscribe(topic string, fn interface{}) error
}

// endregion dependencies

type Manager struct {
	cfg            *config.Config
	bus            EventBus
	db             InterfaceAndPeerDatabaseRepo
	wg             *ControllerManager
	quick          WgQuickController
	statsCollector *StatisticsCollector
	userLockMap    *sync.Map
}

func NewWireGuardManager(
	cfg *config.Config,
	bus EventBus,
	wg *ControllerManager,
	quick WgQuickController,
	db InterfaceAndPeerDatabaseRepo,
	statsCollector *StatisticsCollector,
) (*Manager, error) {
	m := &Manager{
		 cfg:            cfg,
		 bus:            bus,
		 wg:             wg,
		 db:             db,
		 quick:          quick,
		 statsCollector: statsCollector,
		 userLockMap:    &sync.Map{},
	}

	m.connectToMessageBus()

	return m, nil
}

// StartBackgroundJobs starts background jobs like the expired peers check.
// This method is non-blocking.
func (m Manager) StartBackgroundJobs(ctx context.Context) {
	go m.runExpiredPeersCheck(ctx)
	go m.initializePeerTTL(ctx)
}

func (m Manager) connectToMessageBus() {
	_ = m.bus.Subscribe(app.TopicUserCreated, m.handleUserCreationEvent)
	_ = m.bus.Subscribe(app.TopicAuthLogin, m.handleUserLoginEvent)
	_ = m.bus.Subscribe(app.TopicUserDisabled, m.handleUserDisabledEvent)
	_ = m.bus.Subscribe(app.TopicUserEnabled, m.handleUserEnabledEvent)
	_ = m.bus.Subscribe(app.TopicPeerStateChanged, m.handlePeerStateChangeEvent)
	_ = m.bus.Subscribe(app.TopicUserDeleted, m.handleUserDeletedEvent)
	_ = m.bus.Subscribe(app.TopicPeerInterfaceUpdated, m.handlePeerInterfaceUpdatedEvent)
}

func (m Manager) handleUserCreationEvent(user domain.User) {
	if !m.cfg.Core.CreateDefaultPeerOnCreation {
		return
	}

	_, loaded := m.userLockMap.LoadOrStore(user.Identifier, "create")
	if loaded {
		return // another goroutine is already handling this user
	}
	defer m.userLockMap.Delete(user.Identifier)

	slog.Debug("handling new user event", "user", user.Identifier)

	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	err := m.CreateDefaultPeer(ctx, user.Identifier)
	if err != nil {
		slog.Error("failed to create default peer", "user", user.Identifier, "error", err)
		return
	}
}

func (m Manager) handleUserLoginEvent(userId domain.UserIdentifier) {
	if !m.cfg.Core.CreateDefaultPeer {
		return
	}

	_, loaded := m.userLockMap.LoadOrStore(userId, "login")
	if loaded {
		return // another goroutine is already handling this user
	}
	defer m.userLockMap.Delete(userId)

	userPeers, err := m.db.GetUserPeers(context.Background(), userId)
	if err != nil {
		slog.Error("failed to retrieve existing peers prior to default peer creation",
			"user", userId,
			"error", err)
		return
	}

	if len(userPeers) > 0 {
		return // user already has peers, skip creation
	}

	slog.Debug("handling new user login", "user", userId)

	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	err = m.CreateDefaultPeer(ctx, userId)
	if err != nil {
		slog.Error("failed to create default peer", "user", userId, "error", err)
		return
	}
}

func (m Manager) handleUserDisabledEvent(user domain.User) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	userPeers, err := m.db.GetUserPeers(ctx, user.Identifier)
	if err != nil {
		slog.Error("failed to retrieve peers for disabled user",
			"user", user.Identifier,
			"error", err)
		return
	}

	for _, peer := range userPeers {
		if peer.IsDisabled() {
			continue // peer is already disabled
		}

		slog.Debug("disabling peer due to user being disabled",
			"peer", peer.Identifier,
			"user", user.Identifier)

		peer.Disabled = user.Disabled // set to user disabled timestamp
		peer.DisabledReason = domain.DisabledReasonUserDisabled

		_, err := m.UpdatePeer(ctx, &peer)
		if err != nil {
			slog.Error("failed to disable peer for disabled user",
				"peer", peer.Identifier,
				"user", user.Identifier,
				"error", err)
		}
	}
}

func (m Manager) handleUserEnabledEvent(user domain.User) {
	if !m.cfg.Core.ReEnablePeerAfterUserEnable {
		return
	}

	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	userPeers, err := m.db.GetUserPeers(ctx, user.Identifier)
	if err != nil {
		slog.Error("failed to retrieve peers for re-enabled user",
			"user", user.Identifier,
			"error", err)
		return
	}

	for _, peer := range userPeers {
		if !peer.IsDisabled() {
			continue // peer is already active
		}

		if peer.DisabledReason != domain.DisabledReasonUserDisabled {
			continue // peer was disabled for another reason
		}

		slog.Debug("enabling peer due to user being enabled",
			"peer", peer.Identifier,
			"user", user.Identifier)

		peer.Disabled = nil
		peer.DisabledReason = ""

		_, err := m.UpdatePeer(ctx, &peer)
		if err != nil {
			slog.Error("failed to enable peer for enabled user",
				"peer", peer.Identifier,
				"user", user.Identifier,
				"error", err)
		}
	}
	return
}

func (m Manager) handleUserDeletedEvent(user domain.User) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	userPeers, err := m.db.GetUserPeers(ctx, user.Identifier)
	if err != nil {
		slog.Error("failed to retrieve peers for deleted user",
			"user", user.Identifier,
			"error", err)
		return
	}

	deletionTime := time.Now()
	for _, peer := range userPeers {
		if peer.IsDisabled() {
			continue // peer is already disabled
		}

		if m.cfg.Core.DeletePeerAfterUserDeleted {
			slog.Debug("deleting peer due to user being deleted",
				"peer", peer.Identifier,
				"user", user.Identifier)

			if err := m.DeletePeer(ctx, peer.Identifier); err != nil {
				slog.Error("failed to delete peer for deleted user",
					"peer", peer.Identifier,
					"user", user.Identifier,
					"error", err)
			}
		} else {
			slog.Debug("disabling peer due to user being deleted",
				"peer", peer.Identifier,
				"user", user.Identifier)

			peer.UserIdentifier = "" // remove user reference
			peer.Disabled = &deletionTime
			peer.DisabledReason = domain.DisabledReasonUserDeleted

			_, err := m.UpdatePeer(ctx, &peer)
			if err != nil {
				slog.Error("failed to disable peer for deleted user",
					"peer", peer.Identifier,
					"user", user.Identifier,
					"error", err)
			}
		}
	}
}

func (m Manager) runExpiredPeersCheck(ctx context.Context) {
	ctx = domain.SetUserInfo(ctx, domain.SystemAdminContextUserInfo())

	running := true
	for running {
		select {
		case <-ctx.Done():
			running = false
			continue
		case <-time.After(m.cfg.Advanced.ExpiryCheckInterval):
			// select blocks until one of the cases evaluate to true
		}

		interfaces, err := m.db.GetAllInterfaces(ctx)
		if err != nil {
			slog.Error("failed to fetch all interfaces for expiry check", "error", err)
			continue
		}

		for _, iface := range interfaces {
			peers, err := m.db.GetInterfacePeers(ctx, iface.Identifier)
			if err != nil {
				slog.Error("failed to fetch all peers from interface for expiry check",
					"interface", iface.Identifier,
					"error", err)
				continue
			}

			m.checkExpiredPeers(ctx, peers)
		}
	}
}

func (m Manager) checkExpiredPeers(ctx context.Context, peers []domain.Peer) {
	now := time.Now()

	for _, peer := range peers {
		if peer.IsExpired() && !peer.IsDisabled() {
			slog.Info("peer has expired, processing", "peer", peer.Identifier)

			if m.cfg.Core.DeleteExpiredPeers {
				slog.Info("deleting expired peer", "peer", peer.Identifier)
				if err := m.DeletePeer(ctx, peer.Identifier); err != nil {
					slog.Error("failed to delete expired peer", "peer", peer.Identifier, "error", err)
				}
			} else {
				slog.Info("disabling expired peer", "peer", peer.Identifier)
				peer.Disabled = &now
				peer.DisabledReason = domain.DisabledReasonExpired

				_, err := m.UpdatePeer(ctx, &peer)
				if err != nil {
					slog.Error("failed to update expired peer", "peer", peer.Identifier, "error", err)
				}
			}

			// Trigger synchronization after processing the peer
			if _, syncErr := m.SyncAllPeersFromDB(ctx); syncErr != nil {
				slog.Error("failed to synchronize peers after processing expired peer", "peer", peer.Identifier, "error", syncErr)
			}
		}
	}
}

func (m Manager) ClearPeers(ctx context.Context, iface domain.InterfaceIdentifier) error {
    return m.clearPeers(ctx, iface)
}

// handlePeerStateChangeEvent handles peer connection state changes and updates TTL accordingly
func (m Manager) handlePeerStateChangeEvent(peerStatus domain.PeerStatus, peer domain.Peer) {
	ctx := domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
	
	slog.Debug("peer state change event received", "peer", peer.Identifier, "connected", peerStatus.IsConnected)
	
	// Parse the default user TTL from config
	ttlDuration, err := config.ParseDurationWithDays(m.cfg.Core.DefaultUserTTL)
	if err != nil {
		slog.Error("failed to parse default user TTL", "error", err)
		return
	}
	
	// Update peer TTL based on connection state
	var newExpiresAt *time.Time
	if peerStatus.IsConnected {
		// Peer connected - extend TTL from now
		expiryTime := time.Now().Add(ttlDuration)
		newExpiresAt = &expiryTime
		slog.Info("peer connected, extending TTL", "peer", peer.Identifier, "new_expires_at", expiryTime.Format(time.RFC3339))
	} else {
		// Peer disconnected - set TTL to expire after the duration from now
		expiryTime := time.Now().Add(ttlDuration)
		newExpiresAt = &expiryTime
		slog.Info("peer disconnected, setting expiration TTL", "peer", peer.Identifier, "expires_at", expiryTime.Format(time.RFC3339))
	}
	
	// Update the peer in database
	updatedPeer := peer
	updatedPeer.ExpiresAt = newExpiresAt
	
	_, err = m.UpdatePeer(ctx, &updatedPeer)
	if err != nil {
		slog.Error("failed to update peer TTL", "peer", peer.Identifier, "error", err)
	}
}

// initializePeerTTL initializes TTL for peers based on their current connection state
func (m Manager) initializePeerTTL(ctx context.Context) {
	ctx = domain.SetUserInfo(ctx, domain.SystemAdminContextUserInfo())
	slog.Debug("initializing peer TTL based on connection states")
	
	// Parse the default user TTL from config
	ttlDuration, err := config.ParseDurationWithDays(m.cfg.Core.DefaultUserTTL)
	if err != nil {
		slog.Error("failed to parse default user TTL during initialization", "error", err)
		return
	}
	
	// Get all interfaces
	interfaces, err := m.db.GetAllInterfaces(ctx)
	if err != nil {
		slog.Error("failed to get all interfaces for TTL initialization", "error", err)
		return
	}
	
	// Process peers from all interfaces
	for _, iface := range interfaces {
		_, peers, err := m.db.GetInterfaceAndPeers(ctx, iface.Identifier)
		if err != nil {
			slog.Error("failed to get peers for interface", "interface", iface.Identifier, "error", err)
			continue
		}
		
		for _, peer := range peers {
			if peer.IsDisabled() {
				continue // skip disabled peers
			}
			
			// Get peer status to check connection state
			peerStats, err := m.db.GetPeersStats(ctx, peer.Identifier)
			if err != nil || len(peerStats) == 0 {
				continue
			}
			
			peerStatus := peerStats[0]
			
			// Only update TTL for disconnected peers without expiration or with expired TTL
			if !peerStatus.IsConnected && (peer.ExpiresAt == nil || peer.IsExpired()) {
				expiryTime := time.Now().Add(ttlDuration)
				updatedPeer := peer
				updatedPeer.ExpiresAt = &expiryTime
				
				_, err = m.UpdatePeer(ctx, &updatedPeer)
				if err != nil {
					slog.Error("failed to initialize peer TTL", "peer", peer.Identifier, "error", err)
				} else {
					slog.Info("initialized TTL for disconnected peer", "peer", peer.Identifier, "expires_at", expiryTime.Format(time.RFC3339))
				}
			}
		}
	}
}

func (m Manager) handlePeerInterfaceUpdatedEvent(interfaceId domain.InterfaceIdentifier) {
	ctx := context.Background()
	
	slog.Debug("handling peer interface updated event for WireGuard sync", "interface", interfaceId)
	
	// Get current peers from database
	dbPeers, err := m.db.GetInterfacePeers(ctx, interfaceId)
	if err != nil {
		slog.Error("failed to get interface peers from DB", "interface", interfaceId, "error", err)
		return
	}
	
	slog.Debug("WireGuard sync found peers in DB", "interface", interfaceId, "count", len(dbPeers))
	for _, peer := range dbPeers {
		slog.Debug("DB peer", "interface", interfaceId, "peer", peer.Identifier, "disabled", peer.IsDisabled())
	}
	
	// Get local controller
	localController := m.wg.GetControllerByName(config.LocalBackendName)
	if localController == nil {
		slog.Error("local interface controller not found")
		return
	}
	
	// Get current peers from WireGuard device
	wgPeers, err := localController.GetPeers(ctx, interfaceId)
	if err != nil {
		slog.Error("failed to get peers from WireGuard device", "interface", interfaceId, "error", err)
		return
	}
	
	slog.Debug("WireGuard sync found peers in WG device", "interface", interfaceId, "count", len(wgPeers))
	for _, peer := range wgPeers {
		slog.Debug("WG peer", "interface", interfaceId, "peer", peer.Identifier)
	}
	
	// Create maps for easier comparison
	dbPeerMap := make(map[domain.PeerIdentifier]domain.Peer)
	for _, peer := range dbPeers {
		dbPeerMap[peer.Identifier] = peer
	}
	
	wgPeerMap := make(map[domain.PeerIdentifier]domain.PhysicalPeer)
	for _, peer := range wgPeers {
		wgPeerMap[peer.Identifier] = peer
	}
	
	// Remove peers that exist in WireGuard but not in DB
	for wgPeerID := range wgPeerMap {
		if _, exists := dbPeerMap[wgPeerID]; !exists {
			slog.Debug("removing peer from WireGuard device", "interface", interfaceId, "peer", wgPeerID)
			err := localController.DeletePeer(ctx, interfaceId, wgPeerID)
			if err != nil {
				slog.Error("failed to remove peer from WireGuard device", "interface", interfaceId, "peer", wgPeerID, "error", err)
			}
		}
	}
	
	// Add or update peers that exist in DB but not in WireGuard or are different
	for _, dbPeer := range dbPeers {
		if dbPeer.IsDisabled() {
			// If peer is disabled in DB, make sure it's removed from WireGuard
			if _, exists := wgPeerMap[dbPeer.Identifier]; exists {
				slog.Debug("removing disabled peer from WireGuard device", "interface", interfaceId, "peer", dbPeer.Identifier)
				err := localController.DeletePeer(ctx, interfaceId, dbPeer.Identifier)
				if err != nil {
					slog.Error("failed to remove disabled peer from WireGuard device", "interface", interfaceId, "peer", dbPeer.Identifier, "error", err)
				}
			}
			continue
		}
		
		// Peer is enabled, make sure it exists in WireGuard with correct configuration
		slog.Debug("syncing peer in WireGuard device", "interface", interfaceId, "peer", dbPeer.Identifier)
		err := localController.SavePeer(ctx, interfaceId, dbPeer.Identifier, func(pp *domain.PhysicalPeer) (*domain.PhysicalPeer, error) {
			// Convert domain.Peer to domain.PhysicalPeer format
			pp.Identifier = dbPeer.Identifier
			pp.Endpoint = dbPeer.Endpoint.Value
			
			// Parse allowed IPs
			allowedIPs := []domain.Cidr{}
			if dbPeer.AllowedIPsStr.Value != "" {
				for _, ip := range strings.Split(dbPeer.AllowedIPsStr.Value, ",") {
					trimmedIP := strings.TrimSpace(ip)
					if trimmedIP != "" {
						cidr, err := domain.CidrFromString(trimmedIP)
						if err == nil {
							allowedIPs = append(allowedIPs, cidr)
						}
					}
				}
			}
			pp.AllowedIPs = allowedIPs
			
			pp.PresharedKey = dbPeer.PresharedKey
			pp.PersistentKeepalive = dbPeer.PersistentKeepalive.Value
			
			return pp, nil
		})
		if err != nil {
			slog.Error("failed to sync peer in WireGuard device", "interface", interfaceId, "peer", dbPeer.Identifier, "error", err)
		}
	}
	
	slog.Debug("completed WireGuard interface sync", "interface", interfaceId)
}