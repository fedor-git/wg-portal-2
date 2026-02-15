package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-pkgz/routegroup"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/app/api/core/request"
	"github.com/fedor-git/wg-portal-2/internal/app/api/core/respond"
	"github.com/fedor-git/wg-portal-2/internal/app/api/v1/models"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type PeerService interface {
	GetForInterface(context.Context, domain.InterfaceIdentifier) ([]domain.Peer, error)
	GetForUser(context.Context, domain.UserIdentifier) ([]domain.Peer, error)
	GetById(context.Context, domain.PeerIdentifier) (*domain.Peer, error)
	GetAllInterfaces(context.Context) ([]domain.Interface, error)
	Prepare(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Peer, error)
	Create(context.Context, *domain.Peer) (*domain.Peer, error)
	Update(context.Context, domain.PeerIdentifier, *domain.Peer) (*domain.Peer, error)
	Delete(context.Context, domain.PeerIdentifier) error
	SyncAllPeersFromDB(ctx context.Context) (int, error)
	SyncAllPeersFromDBWithLock(ctx context.Context, nodeID string) (int, error)
}

type PeerEndpoint struct {
	peers         PeerService
	authenticator Authenticator
	validator     Validator
	bus           app.EventBus
}

func NewPeerEndpoint(
	authenticator Authenticator,
	validator Validator, peerService PeerService,
) *PeerEndpoint {
	return &PeerEndpoint{
		authenticator: authenticator,
		validator:     validator,
		peers:         peerService,
	}
}

func (e PeerEndpoint) GetName() string {
	return "PeerEndpoint"
}

func (e *PeerEndpoint) SetEventBus(bus app.EventBus) {
	e.bus = bus
}

func (e *PeerEndpoint) publish(topic string, args ...any) {
	if e.bus == nil || topic == "" {
		return
	}
	slog.Debug("[V1] publish", "topic", topic)
	e.bus.Publish(topic, args...)
}

func (e PeerEndpoint) RegisterRoutes(g *routegroup.Bundle) {
	apiGroup := g.Mount("/peer")
	apiGroup.Use(e.authenticator.LoggedIn())

	apiGroup.With(e.authenticator.LoggedIn(ScopeAdmin)).HandleFunc("GET /by-interface/{id}",
		e.handleAllForInterfaceGet())
	apiGroup.HandleFunc("GET /by-user/{id}", e.handleAllForUserGet())
	apiGroup.HandleFunc("GET /by-id/{id}", e.handleByIdGet())

	apiGroup.With(e.authenticator.LoggedIn(ScopeAdmin)).HandleFunc("GET /prepare/{id}", e.handlePrepareGet())
	apiGroup.With(e.authenticator.LoggedIn(ScopeAdmin)).HandleFunc("POST /new", e.handleCreatePost())
	apiGroup.With(e.authenticator.LoggedIn(ScopeAdmin)).HandleFunc("PUT /by-id/{id}", e.handleUpdatePut())
	apiGroup.With(e.authenticator.LoggedIn(ScopeAdmin)).HandleFunc("DELETE /by-id/{id}", e.handleDelete())

	// POST /sync is for node-to-node fanout calls (no authentication required)
	// These are server-to-server sync calls via fanout, not user-initiated
	// Fanout includes auth header for optional validation if needed
	apiGroup.HandleFunc("POST /sync", e.handleSyncPost())

}

// handleAllForInterfaceGet returns a gorm Handler function.
//
// @ID peers_handleAllForInterfaceGet
// @Tags Peers
// @Summary Get all peer records for a given WireGuard interface.
// @Param id path string true "The WireGuard interface identifier."
// @Produce json
// @Success 200 {object} []models.Peer
// @Failure 401 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/by-interface/{id} [get]
// @Security BasicAuth
func (e PeerEndpoint) handleAllForInterfaceGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing interface id"})
			return
		}

		interfacePeers, err := e.peers.GetForInterface(r.Context(), domain.InterfaceIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.JSON(w, http.StatusOK, models.NewPeers(interfacePeers))
	}
}

// handleAllForUserGet returns a gorm Handler function.
//
// @ID peers_handleAllForUserGet
// @Tags Peers
// @Summary Get all peer records for a given user.
// @Description Normal users can only access their own records. Admins can access all records.
// @Param id path string true "The user identifier."
// @Produce json
// @Success 200 {object} []models.Peer
// @Failure 401 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/by-user/{id} [get]
// @Security BasicAuth
func (e PeerEndpoint) handleAllForUserGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing user id"})
			return
		}

		interfacePeers, err := e.peers.GetForUser(r.Context(), domain.UserIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.JSON(w, http.StatusOK, models.NewPeers(interfacePeers))
	}
}

// handleByIdGet returns a gorm Handler function.
//
// @ID peers_handleByIdGet
// @Tags Peers
// @Summary Get a specific peer record by its identifier (public key).
// @Description Normal users can only access their own records. Admins can access all records.
// @Param id path string true "The peer identifier (public key)."
// @Produce json
// @Success 200 {object} models.Peer
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/by-id/{id} [get]
// @Security BasicAuth
func (e PeerEndpoint) handleByIdGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing peer id"})
			return
		}

		peer, err := e.peers.GetById(r.Context(), domain.PeerIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.JSON(w, http.StatusOK, models.NewPeer(peer))
	}
}

// handlePrepareGet returns a gorm handler function.
//
// @ID peers_handlePrepareGet
// @Tags Peers
// @Summary Prepare a new peer record for the given WireGuard interface.
// @Description This endpoint is used to prepare a new peer record. The returned data contains a fresh key pair and valid ip address.
// @Param id path string true "The interface identifier."
// @Produce json
// @Success 200 {object} models.Peer
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/prepare/{id} [get]
// @Security BasicAuth
func (e PeerEndpoint) handlePrepareGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing interface id"})
			return
		}

		peer, err := e.peers.Prepare(r.Context(), domain.InterfaceIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.JSON(w, http.StatusOK, models.NewPeer(peer))
	}
}

// handleCreatePost returns a gorm handler function.
//
// @ID peers_handleCreatePost
// @Tags Peers
// @Summary Create a new peer record.
// @Description Only admins can create new records. The peer record must contain all required fields (e.g., public key, allowed IPs).
// @Param request body models.Peer true "The peer data."
// @Produce json
// @Success 200 {object} models.Peer
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 409 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/new [post]
// @Security BasicAuth
func (e PeerEndpoint) handleCreatePost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var peer models.Peer
		if err := request.BodyJson(r, &peer); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}

		// Log the received ExpiresAt value for debugging
		if peer.ExpiresAt != "" {
			slog.Debug("Creating peer with ExpiresAt value", "raw_value", peer.ExpiresAt)
		}

		if err := e.validator.Struct(peer); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}

		newPeer, err := e.peers.Create(r.Context(), models.NewDomainPeer(&peer))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		// NOTE: TopicPeerCreatedSync is already published by service layer (wireguard_peers.go:304)
		// DO NOT publish it again here to avoid duplicate events and sync cascades
		e.publish("peer.save", newPeer)
		e.publish("peers.updated", "v1:create")

		respond.JSON(w, http.StatusOK, models.NewPeer(newPeer))
	}
}

// handleUpdatePut returns a gorm handler function.
//
// @ID peers_handleUpdatePut
// @Tags Peers
// @Summary Update a peer record.
// @Description Only admins can update existing records. The peer record must contain all required fields (e.g., public key, allowed IPs).
// @Param id path string true "The peer identifier."
// @Param request body models.Peer true "The peer data."
// @Produce json
// @Success 200 {object} models.Peer
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/by-id/{id} [put]
// @Security BasicAuth
func (e PeerEndpoint) handleUpdatePut() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing peer id"})
			return
		}

		var peer models.Peer
		if err := request.BodyJson(r, &peer); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}

		// Log the received ExpiresAt value for debugging
		if peer.ExpiresAt != "" {
			slog.Debug("Received ExpiresAt value", "raw_value", peer.ExpiresAt, "peer_id", id)
		}

		if err := e.validator.Struct(peer); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}

		updatedPeer, err := e.peers.Update(r.Context(), domain.PeerIdentifier(id), models.NewDomainPeer(&peer))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		// NOTE: TopicPeerUpdatedSync is already published by service layer (wireguard_peers.go:422)
		// DO NOT publish it again here to avoid duplicate events and sync cascades
		e.publish("peer.save", updatedPeer)
		e.publish("peers.updated", "v1:update")

		respond.JSON(w, http.StatusOK, models.NewPeer(updatedPeer))
	}
}

// handleDelete returns a gorm handler function.
//
// @ID peers_handleDelete
// @Tags Peers
// @Summary Delete the peer record.
// @Param id path string true "The peer identifier."
// @Produce json
// @Success 204 "No content if deletion was successful."
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /peer/by-id/{id} [delete]
// @Security BasicAuth
func (e PeerEndpoint) handleDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := request.Path(r, "id")
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing peer id"})
			return
		}

		err := e.peers.Delete(r.Context(), domain.PeerIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		// NOTE: TopicPeerDeletedSync is already published by service layer (wireguard_peers.go:464)
		// DO NOT publish it again here to avoid duplicate events and sync cascades
		e.publish("peer.delete", domain.PeerIdentifier(id))
		e.publish("peers.updated", "v1:delete")

		respond.Status(w, http.StatusNoContent)
	}
}

func (e PeerEndpoint) handleSyncPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Header.Get("X-WGP-NoEcho") == "1" {
			ctx = app.WithNoFanout(ctx)
			slog.Debug("/api/v1/peer/sync: NoEcho header detected, disabling fanout")
		}

		// Get node ID from header (identifies which node is sending the sync request)
		nodeID := r.Header.Get("X-WGP-Origin")
		if nodeID == "" {
			// Fallback: try to use hostname as node identifier
			hostname, _ := os.Hostname()
			nodeID = hostname
			if nodeID == "" {
				nodeID = "unknown-node"
			}
		}

		// Check if this is a peer-specific sync (from fanout peer event)
		// or full sync (from periodic interface event or startup)
		peerID := r.URL.Query().Get("peer_id")
		eventType := r.URL.Query().Get("event") // "created", "updated", or "deleted"
		noEcho := r.Header.Get("X-WGP-NoEcho") == "1"

		if peerID != "" {
			// Peer-specific sync: sync only this one peer to local WireGuard
			slog.Info("/api/v1/peer/sync: peer-specific sync",
				"peer_id", peerID, "event", eventType, "node_id", nodeID, "from_fanout", noEcho)

			// Get the specific peer from database
			peer, err := e.peers.GetById(ctx, domain.PeerIdentifier(peerID))
			if err != nil {
				// Peer not found - this is normal for deletion events (peer already deleted)
				// Return 204 No Content instead of 404 error
				slog.Debug("/api/v1/peer/sync: peer not found (already deleted)",
					"peer_id", peerID, "event", eventType, "error", err)

				// If this is a deletion event, trigger local deletion
				if eventType == "deleted" {
					slog.Info("/api/v1/peer/sync: triggering local deletion event for missing peer",
						"peer_id", peerID)
					e.bus.Publish(app.TopicPeerSyncedLocal, domain.PeerIdentifier(peerID))
				}
				respond.Status(w, http.StatusNoContent)
				return
			}

			// Publish to local sync event (NOT fanned out by fanout subscriber)
			// This triggers local WireGuard manager to sync the peer without creating a feedback loop
			//
			// Key difference from TopicPeerCreatedSync:
			// - TopicPeerCreatedSync: Published by creation handler, triggers fanout
			// - TopicPeerSyncedLocal: Published by HTTP endpoint, does NOT trigger fanout
			//
			// Flow:
			// 1. User creates peer on Node A
			// 2. Peer saved to replicated database
			// 3. peer:created:sync published on Node A
			// 4. Node A's WireGuard manager handles (adds to WG)
			// 5. Node A's fanout sends HTTP to nodes B-Z with X-WGP-NoEcho=1
			// 6. Node B HTTP endpoint receives call, publishes TopicPeerSyncedLocal
			// 7. Node B's WireGuard manager handles TopicPeerSyncedLocal (adds to WG)
			// 8. fanout does NOT re-fanout(doesn't subscribe to TopicPeerSyncedLocal)
			// 9. Result: No loop, all nodes have peer
			slog.Debug("/api/v1/peer/sync: publishing local sync event (breaks fanout loop)",
				"peer_id", peerID, "event", eventType, "interface", peer.InterfaceIdentifier)

			e.bus.Publish(app.TopicPeerSyncedLocal, peer.Identifier)

			slog.Info("/api/v1/peer/sync: peer-specific sync completed",
				"peer_id", peerID, "event", eventType, "interface", peer.InterfaceIdentifier,
				"from_fanout", noEcho)

			respond.JSON(w, http.StatusOK, map[string]any{"synced": 1, "peer_id": peerID, "event": eventType})
			return
		}

		// Full sync: use distributed lock to ensure only one node syncs at a time
		// This prevents database contention and 504 Gateway Timeout cascades
		count, err := e.peers.SyncAllPeersFromDBWithLock(ctx, nodeID)
		if err != nil {
			slog.Error("/api/v1/peer/sync: SyncAllPeersFromDBWithLock failed", "error", err, "node_id", nodeID)
			respond.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		slog.Info("/api/v1/peer/sync: full sync completed", "synced", count, "node_id", nodeID)

		// Publish interface update events ONLY if fanout is enabled
		// If X-WGP-NoEcho header is set, NoFanout is true, so skip events to prevent feedback loop
		// (the remote fanout node is just syncing the database, not requesting config updates)
		if !app.NoFanout(ctx) {
			interfaces, err := e.peers.GetAllInterfaces(ctx)
			if err != nil {
				slog.Warn("failed to get interfaces for update events", "error", err)
				// Fallback to hardcoded interface if we can't get interfaces
				e.publish(app.TopicPeerInterfaceUpdated, domain.InterfaceIdentifier("wg0"))
			} else {
				for _, iface := range interfaces {
					e.publish(app.TopicPeerInterfaceUpdated, domain.InterfaceIdentifier(iface.Identifier))
				}
			}
		}

		respond.JSON(w, http.StatusOK, map[string]any{"synced": count})
	}
}
