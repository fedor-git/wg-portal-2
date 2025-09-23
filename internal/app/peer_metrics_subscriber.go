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
			slog.Error("Failed to fetch peer for metrics removal", "peerID", peerID, "error", err)
			return
		}
		metricsServer.RemovePeerMetrics(peer)
		slog.Info("Metrics removed for peer", "peerID", peerID)
	})
}