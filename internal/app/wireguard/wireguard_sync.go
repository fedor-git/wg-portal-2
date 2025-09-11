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
    if m.db == nil { return 0, fmt.Errorf("db repo is nil") }
    if m.wg == nil { return 0, fmt.Errorf("wg controller is nil") }

    ifaces, err := m.db.GetAllInterfaces(ctx)
    if err != nil {
        return 0, fmt.Errorf("list interfaces: %w", err)
    }

    applied := 0
    for _, in := range ifaces {
        if err := m.RestoreInterfaceState(ctx, true, in.Identifier); err != nil {
            slog.ErrorContext(ctx, "restore interface state failed", "iface", in.Identifier, "err", err)
            continue
        }
        peers, err := m.db.GetInterfacePeers(ctx, in.Identifier)
        if err != nil {
            slog.ErrorContext(ctx, "peer sync: failed to load peers", "iface", in.Identifier, "err", err)
            continue
        }
        slog.Debug("SyncAllPeersFromDB: loaded peers from DB", "iface", in.Identifier, "count", len(peers), "peers", peers)
        if len(peers) == 0 {
            if err := m.wg.ClearPeers(ctx, string(in.Identifier)); err != nil {
                slog.ErrorContext(ctx, "clear peers failed", "iface", in.Identifier, "err", err)
            }
            if !app.NoFanout(ctx) && m.bus != nil {
                m.bus.Publish("peers.updated", "sync")
            }
            continue
        }
        desired := make([]domain.Peer, 0, len(peers))
        for i := range peers {
            if !peers[i].IsDisabled() {
                desired = append(desired, peers[i])
            } else {
                slog.Debug("SyncAllPeersFromDB: peer is disabled, skipping", "peer", peers[i])
            }
        }
        if err := m.replacePeers(ctx, in.Identifier, desired); err != nil {
            if isNoSuchFile(err) {
                slog.WarnContext(ctx, "replacePeers failed (no iface/file), restoring and retrying",
                    "iface", in.Identifier, "err", err)
                if rErr := m.RestoreInterfaceState(ctx, true, in.Identifier); rErr != nil {
                    slog.ErrorContext(ctx, "retry restore interface failed", "iface", in.Identifier, "err", rErr)
                    continue
                }
                if r2 := m.replacePeers(ctx, in.Identifier, desired); r2 != nil {
                    slog.ErrorContext(ctx, "replacePeers retry failed", "iface", in.Identifier, "err", r2)
                    continue
                }
            } else {
                slog.ErrorContext(ctx, "replacePeers failed", "iface", in.Identifier, "err", err)
                continue
            }
        }
        if !app.NoFanout(ctx) && m.bus != nil {
            m.bus.Publish("peers.updated", "sync")
        }
        slog.Debug("SyncAllPeersFromDB called", "ctx", ctx)
        applied += len(desired)
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
        if p.IsDisabled() { continue }
        if err := m.savePeers(ctx, p); err != nil {
            if firstErr == nil {
                firstErr = fmt.Errorf("apply peer %s (iface %s): %w", p.Identifier, p.InterfaceIdentifier, err)
            }
            continue
        }
        // <-- тут головне
        if !app.NoFanout(ctx) {
            m.bus.Publish(app.TopicPeerUpdated)
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