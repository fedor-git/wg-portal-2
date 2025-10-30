package app

import (
	"context"

	"github.com/fedor-git/wg-portal-2/internal/adapters"
	"github.com/fedor-git/wg-portal-2/internal/domain"
	"golang.org/x/exp/slog"
)

// SubscribeToPeerDelete subscribes to the peer.delete topic and handles metrics removal
func SubscribeToPeerDelete(bus EventBus, metricsServer *adapters.MetricsServer, repo *adapters.SqlRepo) {
	bus.Subscribe(TopicFanPeerDelete, func(peerID domain.PeerIdentifier) {
		ctx := context.Background()
		peer, err := repo.GetPeer(ctx, peerID)
		if err != nil {
			slog.Debug("Peer not found for metrics removal, removing by ID", "peerID", peerID, "error", err)
			// Peer already deleted from DB, remove metrics by ID directly
			metricsServer.RemovePeerMetricsByID(string(peerID))
			slog.Info("Metrics removed for peer by ID", "peerID", peerID)
			return
		}
		metricsServer.RemovePeerMetrics(peer)
		slog.Info("Metrics removed for peer", "peerID", peerID)
	})
}