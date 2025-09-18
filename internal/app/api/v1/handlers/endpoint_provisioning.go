package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-pkgz/routegroup"

	"github.com/fedor-git/wg-portal-2/internal/app"
	"github.com/fedor-git/wg-portal-2/internal/app/api/core/request"
	"github.com/fedor-git/wg-portal-2/internal/app/api/core/respond"
	"github.com/fedor-git/wg-portal-2/internal/app/api/v1/models"
	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type ProvisioningEndpointProvisioningService interface {
	GetUserAndPeers(ctx context.Context, userId domain.UserIdentifier, email string) (
		*domain.User,
		[]domain.Peer,
		error,
	)
	GetPeerConfig(ctx context.Context, peerId domain.PeerIdentifier) ([]byte, error)
	GetPeerQrPng(ctx context.Context, peerId domain.PeerIdentifier) ([]byte, error)
	NewPeer(ctx context.Context, req models.ProvisioningRequest) (*domain.Peer, error)
	FindPeerByDisplayNameAndUserIdentifier(ctx context.Context, displayName, userIdentifier string) (*domain.Peer, error)
	GetConfig() *config.Config
}

type ProvisioningEndpoint struct {
	provisioning  ProvisioningEndpointProvisioningService
	authenticator Authenticator
	validator     Validator
	bus           app.EventPublisher
}

func NewProvisioningEndpoint(
	authenticator Authenticator,
	validator Validator,
	provisioning ProvisioningEndpointProvisioningService,
	bus app.EventPublisher,
) *ProvisioningEndpoint {
	return &ProvisioningEndpoint{
		authenticator: authenticator,
		validator:     validator,
		provisioning:  provisioning,
		bus:           bus,
	}
}

func (e ProvisioningEndpoint) GetName() string {
	return "ProvisioningEndpoint"
}

func (e ProvisioningEndpoint) RegisterRoutes(g *routegroup.Bundle) {
	apiGroup := g.Mount("/provisioning")
	apiGroup.Use(e.authenticator.LoggedIn())

	apiGroup.HandleFunc("GET /data/user-info", e.handleUserInfoGet())
	apiGroup.HandleFunc("GET /data/peer-config", e.handlePeerConfigGet())
	apiGroup.HandleFunc("GET /data/peer-qr", e.handlePeerQrGet())

	apiGroup.HandleFunc("POST /new-peer", e.handleNewPeerPost())
}

// handleUserInfoGet returns a gorm Handler function.
//
// @ID provisioning_handleUserInfoGet
// @Tags Provisioning
// @Summary Get information about all peer records for a given user.
// @Description Normal users can only access their own record. Admins can access all records.
// @Param UserId query string false "The user identifier that should be queried. If not set, the authenticated user is used."
// @Param Email query string false "The email address that should be queried. If UserId is set, this is ignored."
// @Produce json
// @Success 200 {object} models.UserInformation
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /provisioning/data/user-info [get]
// @Security BasicAuth
func (e ProvisioningEndpoint) handleUserInfoGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(request.Query(r, "UserId"))
		email := strings.TrimSpace(request.Query(r, "Email"))

		if id == "" && email == "" {
			id = string(domain.GetUserInfo(r.Context()).Id)
		}

		user, peers, err := e.provisioning.GetUserAndPeers(r.Context(), domain.UserIdentifier(id), email)
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.JSON(w, http.StatusOK, models.NewUserInformation(user, peers))
	}
}

// handlePeerConfigGet returns a gorm Handler function.
//
// @ID provisioning_handlePeerConfigGet
// @Tags Provisioning
// @Summary Get the peer configuration in wg-quick format.
// @Description Normal users can only access their own record. Admins can access all records.
// @Param PeerId query string true "The peer identifier (public key) that should be queried."
// @Produce plain
// @Produce json
// @Success 200 {string} string "The WireGuard configuration file"
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /provisioning/data/peer-config [get]
// @Security BasicAuth
func (e ProvisioningEndpoint) handlePeerConfigGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(request.Query(r, "PeerId"))
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing peer id"})
			return
		}

		peerConfig, err := e.provisioning.GetPeerConfig(r.Context(), domain.PeerIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.Data(w, http.StatusOK, "text/plain", peerConfig)
	}
}

// handlePeerQrGet returns a gorm Handler function.
//
// @ID provisioning_handlePeerQrGet
// @Tags Provisioning
// @Summary Get the peer configuration as QR code.
// @Description Normal users can only access their own record. Admins can access all records.
// @Param PeerId query string true "The peer identifier (public key) that should be queried."
// @Produce png
// @Produce json
// @Success 200 {file} binary "The WireGuard configuration QR code"
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /provisioning/data/peer-qr [get]
// @Security BasicAuth
func (e ProvisioningEndpoint) handlePeerQrGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(request.Query(r, "PeerId"))
		if id == "" {
			respond.JSON(w, http.StatusBadRequest,
				models.Error{Code: http.StatusBadRequest, Message: "missing peer id"})
			return
		}

		peerConfigQrCode, err := e.provisioning.GetPeerQrPng(r.Context(), domain.PeerIdentifier(id))
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		respond.Data(w, http.StatusOK, "image/png", peerConfigQrCode)
	}
}

// handleNewPeerPost returns a gorm Handler function.
//
// @ID provisioning_handleNewPeerPost
// @Tags Provisioning
// @Summary Create a new peer for the given interface and user.
// @Description Normal users can only create new peers if self provisioning is allowed. Admins can always add new peers.
// @Param request body models.ProvisioningRequest true "Provisioning request model."
// @Produce json
// @Success 200 {object} models.Peer
// @Failure 400 {object} models.Error
// @Failure 401 {object} models.Error
// @Failure 403 {object} models.Error
// @Failure 404 {object} models.Error
// @Failure 500 {object} models.Error
// @Router /provisioning/new-peer [post]
// @Security BasicAuth
func (e ProvisioningEndpoint) handleNewPeerPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req models.ProvisioningRequest
		if err := request.BodyJson(r, &req); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}
		if err := e.validator.Struct(req); err != nil {
			respond.JSON(w, http.StatusBadRequest, models.Error{Code: http.StatusBadRequest, Message: err.Error()})
			return
		}

		// Set default Expiry date if not provided
		if req.ExpiresAt == "" {
			ttl := e.provisioning.GetConfig().Core.DefaultUserTTL
			currentDate := time.Now()
			expiryDate := currentDate.AddDate(0, 0, ttl) // Add TTL days to the current date
			req.ExpiresAt = expiryDate.Format(models.ExpiryDateTimeLayout)
		}

		// Check for existing peer with the same DisplayName and UserIdentifier
		existingPeer, err := e.provisioning.FindPeerByDisplayNameAndUserIdentifier(r.Context(), req.DisplayName, req.UserIdentifier)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}
		if existingPeer != nil {
			respond.JSON(w, http.StatusOK, struct {
				Status string        `json:"status"`
				Peer   models.Peer   `json:"peer"`
			}{
				Status: "existing",
				Peer:   *models.NewPeer(existingPeer),
			})
			return
		}

		peer, err := e.provisioning.NewPeer(r.Context(), req)
		if err != nil {
			status, model := ParseServiceError(err)
			respond.JSON(w, status, model)
			return
		}

		if e.bus != nil {
			e.bus.Publish("peers.updated", "provisioning:new-peer")
		}

		respond.JSON(w, http.StatusOK, models.NewPeer(peer))
	}
}