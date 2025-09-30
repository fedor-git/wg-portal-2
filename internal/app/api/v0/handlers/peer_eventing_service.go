package handlers

import (
	"context"
	"io"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type eventingPeerService struct {
	inner PeerService
	bus   app.EventPublisher
}

func NewEventingPeerService(inner PeerService, bus app.EventPublisher) PeerService {
	return &eventingPeerService{inner: inner, bus: bus}
}

func (s *eventingPeerService) GetInterfaceAndPeers(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, []domain.Peer, error) {
	return s.inner.GetInterfaceAndPeers(ctx, id)
}

func (s *eventingPeerService) PreparePeer(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Peer, error) {
	return s.inner.PreparePeer(ctx, id)
}

func (s *eventingPeerService) GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error) {
	return s.inner.GetPeer(ctx, id)
}

func (s *eventingPeerService) GetPeerConfig(ctx context.Context, id domain.PeerIdentifier, style string) (io.Reader, error) {
	return s.inner.GetPeerConfig(ctx, id, style)
}

func (s *eventingPeerService) GetPeerConfigQrCode(ctx context.Context, id domain.PeerIdentifier, style string) (io.Reader, error) {
	return s.inner.GetPeerConfigQrCode(ctx, id, style)
}

func (s *eventingPeerService) SendPeerEmail(ctx context.Context, linkOnly bool, style string, peers ...domain.PeerIdentifier) error {
	return s.inner.SendPeerEmail(ctx, linkOnly, style, peers...)
}

func (s *eventingPeerService) GetPeerStats(ctx context.Context, id domain.InterfaceIdentifier) ([]domain.PeerStatus, error) {
	return s.inner.GetPeerStats(ctx, id)
}

func (s *eventingPeerService) CreatePeer(ctx context.Context, p *domain.Peer) (*domain.Peer, error) {
    out, err := s.inner.CreatePeer(ctx, p)
    if err != nil { return nil, err }
    
    // Local events for statistics
    s.publish(app.TopicPeerCreated, *out)
    s.publish(app.TopicPeerUpdated, *out)
    
    // Fanout events for sync with other nodes
    s.publish("peer.save", out)
    s.publish("peers.updated", "v0:create")
    
    return out, nil
}

func (s *eventingPeerService) CreateMultiplePeers(ctx context.Context, ifaceID domain.InterfaceIdentifier, r *domain.PeerCreationRequest) ([]domain.Peer, error) {
	out, err := s.inner.CreateMultiplePeers(ctx, ifaceID, r)
	if err != nil { return nil, err }

	s.publish(app.TopicPeerUpdated, out)

	s.publish("peer.save", out)
	s.publish("peers.updated", struct{}{})

	return out, nil
}

func (s *eventingPeerService) UpdatePeer(ctx context.Context, p *domain.Peer) (*domain.Peer, error) {
    out, err := s.inner.UpdatePeer(ctx, p)
    if err != nil { return nil, err }
    
    // Local events for statistics
    s.publish(app.TopicPeerUpdated, *out)
    
    // Fanout events for sync with other nodes
    s.publish("peer.save", out)
    s.publish("peers.updated", "v0:update")
    
    return out, nil
}

func (s *eventingPeerService) DeletePeer(ctx context.Context, id domain.PeerIdentifier) error {
    if err := s.inner.DeletePeer(ctx, id); err != nil { return err }
    
    // Local events for statistics
    s.publish(app.TopicPeerDeleted, id)
    s.publish(app.TopicPeerUpdated, id)
    
    // Fanout events for sync with other nodes
    s.publish("peer.delete", id)
    s.publish("peers.updated", "v0:delete")
    
    return nil
}

func (s *eventingPeerService) publish(topic string, args ...any) {
	if s.bus == nil || topic == "" { return }
	if len(args) == 0 {
		s.bus.Publish(topic, struct{}{})
		return
	}
	if len(args) > 1 {
		s.bus.Publish(topic, args)
		return
	}
	s.bus.Publish(topic, args[0])
}
