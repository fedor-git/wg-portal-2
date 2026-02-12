package wireguard

import (
	"context"
	"testing"
	"time"

	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
	"github.com/stretchr/testify/mock"
)

// Mock EventBus
type MockEventBus struct {
	mock.Mock
}

func (m *MockEventBus) Publish(topic string, args ...any) {
	m.Called(topic, args)
}

func (m *MockEventBus) Subscribe(topic string, fn interface{}) error {
	args := m.Called(topic, fn)
	return args.Error(0)
}

// Mock database
type MockDB struct {
	mock.Mock
}

func (m *MockDB) GetInterface(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(*domain.Interface), args.Error(1)
}

func (m *MockDB) GetInterfaceAndPeers(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, []domain.Peer, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(*domain.Interface), args.Get(1).([]domain.Peer), args.Error(2)
}

func (m *MockDB) GetPeersStats(ctx context.Context, ids ...domain.PeerIdentifier) ([]domain.PeerStatus, error) {
	args := m.Called(ctx, ids)
	return args.Get(0).([]domain.PeerStatus), args.Error(1)
}

func (m *MockDB) GetAllPeers(ctx context.Context) ([]domain.Peer, error) {
	args := m.Called(ctx)
	return args.Get(0).([]domain.Peer), args.Error(1)
}

func (m *MockDB) UpdatePeer(ctx context.Context, peer *domain.Peer) error {
	args := m.Called(ctx, peer)
	return args.Error(0)
}

func (m *MockDB) GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(*domain.Peer), args.Error(1)
}

func (m *MockDB) GetAllInterfaces(ctx context.Context) ([]domain.Interface, error) {
	args := m.Called(ctx)
	return args.Get(0).([]domain.Interface), args.Error(1)
}

func (m *MockDB) UpdatePeerStatus(ctx context.Context, id domain.PeerIdentifier, updateFunc func(*domain.PeerStatus) (*domain.PeerStatus, error)) error {
	args := m.Called(ctx, id, updateFunc)
	return args.Error(0)
}

func (m *MockDB) ClaimPeerStatus(ctx context.Context, id domain.PeerIdentifier, ownerNodeId string, updateFunc func(*domain.PeerStatus) (*domain.PeerStatus, error)) error {
	args := m.Called(ctx, id, ownerNodeId, updateFunc)
	return args.Error(0)
}

func (m *MockDB) BatchUpdatePeerStatuses(ctx context.Context, updates map[domain.PeerIdentifier]func(*domain.PeerStatus) (*domain.PeerStatus, error)) error {
	args := m.Called(ctx, updates)
	return args.Error(0)
}

func (m *MockDB) GetPeersByUser(ctx context.Context, userID domain.UserIdentifier, enabledOnly bool) ([]domain.Peer, error) {
	args := m.Called(ctx, userID, enabledOnly)
	return args.Get(0).([]domain.Peer), args.Error(1)
}

func (m *MockDB) CreatePeer(ctx context.Context, peer *domain.Peer) error {
	args := m.Called(ctx, peer)
	return args.Error(0)
}

func (m *MockDB) DeletePeer(ctx context.Context, id domain.PeerIdentifier) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func TestManager_handlePeerStateChangeEvent_PeerConnected(t *testing.T) {
	// Setup
	mockBus := &MockEventBus{}
	mockDB := &MockDB{}
	
	cfg := &config.Config{
		Core: config.CoreConfig{
			DefaultUserTTL: "24h",
		},
	}
	
	manager := &Manager{
		cfg: cfg,
		bus: mockBus,
		db:  mockDB,
	}
	
	peerID := domain.PeerIdentifier("test-peer-123")
	peer := domain.Peer{
		Identifier: peerID,
		ExpiresAt:  nil, // Initially no expiration
	}
	
	peerStatus := domain.PeerStatus{
		PeerId:      peerID,
		IsConnected: true, // Peer is connecting
	}
	
	// Mock UpdatePeer call
	mockDB.On("UpdatePeer", mock.Anything, mock.MatchedBy(func(p *domain.Peer) bool {
		// Verify that ExpiresAt is set to approximately 24 hours from now
		if p.ExpiresAt == nil {
			return false
		}
		expectedTime := time.Now().Add(24 * time.Hour)
		timeDiff := p.ExpiresAt.Sub(expectedTime)
		return timeDiff > -time.Minute && timeDiff < time.Minute // Allow 1 minute tolerance
	})).Return(nil)
	
	// Execute
	manager.handlePeerStateChangeEvent(peerStatus, peer)
	
	// Verify
	mockDB.AssertExpectations(t)
}

func TestManager_handlePeerStateChangeEvent_PeerDisconnected(t *testing.T) {
	// Setup
	mockBus := &MockEventBus{}
	mockDB := &MockDB{}
	
	cfg := &config.Config{
		Core: config.CoreConfig{
			DefaultUserTTL: "1h",
		},
	}
	
	manager := &Manager{
		cfg: cfg,
		bus: mockBus,
		db:  mockDB,
	}
	
	peerID := domain.PeerIdentifier("test-peer-456")
	currentTime := time.Now()
	existingExpiry := currentTime.Add(12 * time.Hour)
	
	peer := domain.Peer{
		Identifier: peerID,
		ExpiresAt:  &existingExpiry, // Had existing expiration
	}
	
	peerStatus := domain.PeerStatus{
		PeerId:      peerID,
		IsConnected: false, // Peer is disconnecting
	}
	
	// Mock UpdatePeer call
	mockDB.On("UpdatePeer", mock.Anything, mock.MatchedBy(func(p *domain.Peer) bool {
		// Verify that ExpiresAt is set to approximately 1 hour from now (countdown starts)
		if p.ExpiresAt == nil {
			return false
		}
		expectedTime := time.Now().Add(1 * time.Hour)
		timeDiff := p.ExpiresAt.Sub(expectedTime)
		return timeDiff > -time.Minute && timeDiff < time.Minute // Allow 1 minute tolerance
	})).Return(nil)
	
	// Execute
	manager.handlePeerStateChangeEvent(peerStatus, peer)
	
	// Verify
	mockDB.AssertExpectations(t)
}

func TestManager_handlePeerStateChangeEvent_InvalidTTL(t *testing.T) {
	// Setup
	mockBus := &MockEventBus{}
	mockDB := &MockDB{}
	
	cfg := &config.Config{
		Core: config.CoreConfig{
			DefaultUserTTL: "invalid-ttl", // Invalid TTL format
		},
	}
	
	manager := &Manager{
		cfg: cfg,
		bus: mockBus,
		db:  mockDB,
	}
	
	peerID := domain.PeerIdentifier("test-peer-789")
	peer := domain.Peer{
		Identifier: peerID,
	}
	
	peerStatus := domain.PeerStatus{
		PeerId:      peerID,
		IsConnected: true,
	}
	
	// Should not call UpdatePeer due to invalid TTL
	// mockDB.On("UpdatePeer", ...).Return(...) - Not called
	
	// Execute
	manager.handlePeerStateChangeEvent(peerStatus, peer)
	
	// Verify - no database calls should have been made
	mockDB.AssertNotCalled(t, "UpdatePeer")
}