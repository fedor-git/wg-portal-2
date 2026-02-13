package wireguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type peerLister interface {
	GetAllPeers(ctx context.Context) ([]domain.Peer, error)
}

func (m Manager) SyncAllPeersFromDB(ctx context.Context) (int, error) {
	if err := domain.ValidateAdminAccessRights(ctx); err != nil {
		return 0, err
	}
	if m.db == nil {
		return 0, fmt.Errorf("db repo is nil")
	}
	if m.wg == nil {
		return 0, fmt.Errorf("wg controller is nil")
	}

	// Clean orphaned statuses BEFORE syncing WireGuard
	// This allows us to detect peers in WireGuard that shouldn't be there
	if m.statsCollector != nil {
		slog.Info("SyncAllPeersFromDB: calling CleanOrphanedStatuses BEFORE sync")
		m.statsCollector.CleanOrphanedStatuses(ctx)
	} else {
		slog.Warn("SyncAllPeersFromDB: statsCollector is nil, skipping orphaned cleanup")
	}

	ifaces, err := m.db.GetAllInterfaces(ctx)
	if err != nil {
		return 0, fmt.Errorf("list interfaces: %w", err)
	}

	applied := 0
	for _, in := range ifaces {
		peers, err := m.db.GetInterfacePeers(ctx, in.Identifier)
		if err != nil {
			slog.ErrorContext(ctx, "peer sync: failed to load peers", "iface", in.Identifier, "err", err)
			continue
		}

		existingPeers, err := m.wg.ListPeers(ctx, string(in.Identifier))
		if err != nil {
			slog.ErrorContext(ctx, "failed to list existing peers", "iface", in.Identifier, "err", err)
			continue
		}

		existingPeerMap := make(map[string]domain.Peer)
		for _, p := range existingPeers {
			existingPeerMap[string(p.Identifier)] = p
		}

		newPeerMap := make(map[string]domain.Peer)
		for _, p := range peers {
			if !p.IsDisabled() {
				newPeerMap[string(p.Identifier)] = p
			}
		}

		// Remove peers that are no longer in the database
		for id := range existingPeerMap {
			if _, exists := newPeerMap[id]; !exists {
				if err := m.wg.RemovePeer(ctx, string(in.Identifier), id); err != nil {
					slog.ErrorContext(ctx, "failed to remove peer", "peer", id, "iface", in.Identifier, "err", err)
				}
			}
		}

		// Add or update peers
		for id, peer := range newPeerMap {
			if existingPeer, exists := existingPeerMap[id]; !exists || !existingPeer.Equals(peer) {
				if err := m.wg.SavePeer(ctx, string(in.Identifier), &peer); err != nil {
					slog.ErrorContext(ctx, "failed to save peer", "peer", id, "iface", in.Identifier, "err", err)
					continue
				}
				applied++
			}
		}
	}

	return applied, nil
}

func (m Manager) replacePeers(ctx context.Context, iface domain.InterfaceIdentifier, peers []domain.Peer) error {
	slog.Debug("replacePeers: will write peers to wg0.conf", "iface", iface, "peers_count", len(peers), "peers", peers)

	if err := m.clearPeers(ctx, iface); err != nil {
		return err
	}

	for i := range peers {
		if err := m.savePeers(ctx, &peers[i]); err != nil {
			return fmt.Errorf("add peer %s on %s: %w", peers[i].Identifier, iface, err)
		}
	}

	if m.bus != nil {
		m.bus.Publish(app.TopicPeerInterfaceUpdated, iface)
	}

	return nil
}

func (m Manager) clearPeers(ctx context.Context, iface domain.InterfaceIdentifier) error {
	slog.Debug("clearPeers: clearing all peers from wg0.conf", "iface", iface)
	return m.wg.ClearPeers(ctx, string(iface))
}

func (m Manager) applyPeers(ctx context.Context, peers []domain.Peer) error {
	var firstErr error
	for i := range peers {
		p := &peers[i]
		if p.IsDisabled() {
			continue
		}
		if err := m.savePeers(ctx, p); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("apply peer %s (iface %s): %w",
					p.Identifier, p.InterfaceIdentifier, err)
			}
			continue
		}
		if !app.NoFanout(ctx) && m.bus != nil {
			m.bus.Publish(app.TopicPeerUpdated, struct{}{})
		}
	}
	return firstErr
}

func isNoSuchFile(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "file does not exist")
}

// SyncAllPeersFromDBWithLock syncs all peers from database using distributed lock
// Only one node acquires the lock and performs the sync to prevent database contention
func (m Manager) SyncAllPeersFromDBWithLock(ctx context.Context, nodeID string) (int, error) {
	if err := domain.ValidateAdminAccessRights(ctx); err != nil {
		return 0, err
	}

	// Attempt to acquire distributed lock
	type syncerWithLock interface {
		SyncAllPeersFromDBWithLock(context.Context, string) (int, error)
	}

	if v, ok := any(m.db).(syncerWithLock); ok {
		return v.SyncAllPeersFromDBWithLock(ctx, nodeID)
	}

	// Fallback to regular sync without lock
	slog.Warn("[SYNC] SyncAllPeersFromDBWithLock called but db doesn't support locks, using regular sync", "node_id", nodeID)
	return m.SyncAllPeersFromDB(ctx)
}
