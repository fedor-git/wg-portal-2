package adapters

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"

	"github.com/fedor-git/wg-portal-2/internal"
	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type MetricsServer struct {
	*http.Server

	DB  *gorm.DB        // Database connection
	cfg *config.Config // Configuration

	registry *prometheus.Registry

	ifaceReceivedBytesTotal  *prometheus.GaugeVec
	ifaceSendBytesTotal      *prometheus.GaugeVec
	peerIsConnected          *prometheus.GaugeVec
	peerLastHandshakeSeconds *prometheus.GaugeVec
	peerReceivedBytesTotal   *prometheus.GaugeVec
	peerSendBytesTotal       *prometheus.GaugeVec
}

// Wireguard metrics labels
var (
	ifaceLabels = []string{"interface"}
	peerLabels  = []string{"interface", "addresses", "id", "name"}
)

// NewMetricsServer returns a new prometheus server
func NewMetricsServer(cfg *config.Config, db *gorm.DB) *MetricsServer {
	// Create a new custom registry
       reg := prometheus.NewRegistry()

       mux := http.NewServeMux()
       mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

       ms := &MetricsServer{
	       Server: &http.Server{
		       Addr:    cfg.Statistics.ListeningAddress,
		       Handler: mux,
	       },
	       DB:  db,
	       cfg: cfg,
	       registry: reg,

	       ifaceReceivedBytesTotal: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_interface_received_bytes_total",
			       Help: "Bytes received through the interface.",
		       }, ifaceLabels,
	       ),
	       ifaceSendBytesTotal: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_interface_sent_bytes_total",
			       Help: "Bytes sent through the interface.",
		       }, ifaceLabels,
	       ),

	       peerIsConnected: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_peer_up",
			       Help: "Peer connection state (boolean: 1/0).",
		       }, peerLabels,
	       ),
	       peerLastHandshakeSeconds: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_peer_last_handshake_seconds",
			       Help: "Seconds from the last handshake with the peer.",
		       }, peerLabels,
	       ),
	       peerReceivedBytesTotal: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_peer_received_bytes_total",
			       Help: "Bytes received from the peer.",
		       }, peerLabels,
	       ),
	       peerSendBytesTotal: prometheus.NewGaugeVec(
		       prometheus.GaugeOpts{
			       Name: "wireguard_peer_sent_bytes_total",
			       Help: "Bytes sent to the peer.",
		       }, peerLabels,
	       ),
       }

       reg.MustRegister(
	       ms.ifaceReceivedBytesTotal,
	       ms.ifaceSendBytesTotal,
	       ms.peerIsConnected,
       )

       if cfg.Statistics.ExportDetailedPeerMetrics {
	       reg.MustRegister(
		       ms.peerLastHandshakeSeconds,
		       ms.peerReceivedBytesTotal,
		       ms.peerSendBytesTotal,
	       )
	       slog.Info("Detailed peer metrics enabled (handshake, bytes received/transmitted)")
       } else {
	       slog.Info("Detailed peer metrics disabled (only peer_up will be exported)")
       }

       return ms
}

// Run starts the metrics server. The function blocks until the context is cancelled.
func (m *MetricsServer) Run(ctx context.Context) {
	// Run the metrics server in a goroutine
	go func() {
		if err := m.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics service exited", "address", m.Addr, "error", err)
		}
	}()

	slog.Info("started metrics service", "address", m.Addr)

	// Wait for the context to be done
	<-ctx.Done()

	// Create a context with timeout for the shutdown process
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt to gracefully shut down the metrics server
	if err := m.Shutdown(shutdownCtx); err != nil {
		slog.Error("metrics service shutdown failed", "address", m.Addr, "error", err)
	} else {
		slog.Info("metrics service shutdown gracefully", "address", m.Addr)
	}
}

// UpdateInterfaceMetrics updates the metrics for the given interface
func (m *MetricsServer) UpdateInterfaceMetrics(status domain.InterfaceStatus) {
	labels := []string{string(status.InterfaceId)}
	m.ifaceReceivedBytesTotal.WithLabelValues(labels...).Set(float64(status.BytesReceived))
	m.ifaceSendBytesTotal.WithLabelValues(labels...).Set(float64(status.BytesTransmitted))

	// Add debug logs for interface metrics registration
	slog.Debug("Registering interface metrics", "labels", labels, "interfaceID", status.InterfaceId)
	slog.Debug("Setting ifaceReceivedBytesTotal", "value", status.BytesReceived)
	slog.Debug("Setting ifaceSendBytesTotal", "value", status.BytesTransmitted)
}

// UpdatePeerMetrics updates the metrics for the given peer
func (m *MetricsServer) UpdatePeerMetrics(peer *domain.Peer, status domain.PeerStatus) {
       labels := []string{
	       string(peer.InterfaceIdentifier),
	       peer.CheckAliveAddress(), // addresses label now contains only the ping address
	       string(status.PeerId),
	       peer.DisplayName,
       }

       if labels[2] == "" {
	       slog.Warn("Skip UpdatePeerMetrics: id label is empty", "labels", labels, "peerID", peer.Identifier)
	       return
       }

       // First, remove any existing metrics for this peer ID to prevent duplicates
       // This is necessary because label values (like name or addresses) might have changed
       m.removePeerMetricsByIDInternal(string(status.PeerId))

       // Add debug logs for peer metrics registration
       slog.Debug("Registering peer metrics", "labels", labels, "peerID", peer.Identifier, "name", peer.DisplayName)
       slog.Debug("Setting peerIsConnected", "value", internal.BoolToFloat64(status.IsConnected))
       m.peerIsConnected.WithLabelValues(labels...).Set(internal.BoolToFloat64(status.IsConnected))

       if m.cfg.Statistics.ExportDetailedPeerMetrics {
	       if status.LastHandshake != nil {
		       slog.Debug("Setting peerLastHandshakeSeconds", "value", status.LastHandshake.Unix())
		       m.peerLastHandshakeSeconds.WithLabelValues(labels...).Set(float64(status.LastHandshake.Unix()))
	       }
	       slog.Debug("Setting peerReceivedBytesTotal", "value", status.BytesReceived)
	       slog.Debug("Setting peerSendBytesTotal", "value", status.BytesTransmitted)
	       m.peerReceivedBytesTotal.WithLabelValues(labels...).Set(float64(status.BytesReceived))
	       m.peerSendBytesTotal.WithLabelValues(labels...).Set(float64(status.BytesTransmitted))
       }
}

// removePeerMetricsByIDInternal is an internal method to remove peer metrics without verbose logging
func (m *MetricsServer) removePeerMetricsByIDInternal(peerId string) {
       mfs, err := m.registry.Gather()
       if err != nil {
	       return
       }

       metricMap := map[string]*prometheus.GaugeVec{
	       "wireguard_peer_up": m.peerIsConnected,
       }

       if m.cfg.Statistics.ExportDetailedPeerMetrics {
	       metricMap["wireguard_peer_last_handshake_seconds"] = m.peerLastHandshakeSeconds
	       metricMap["wireguard_peer_received_bytes_total"] = m.peerReceivedBytesTotal
	       metricMap["wireguard_peer_sent_bytes_total"] = m.peerSendBytesTotal
       }

       for _, mf := range mfs {
	       name := mf.GetName()
	       vec, ok := metricMap[name]
	       if !ok {
		       continue
	       }
	       for _, mtr := range mf.GetMetric() {
		       var labelValues []string
		       var found bool
		       for _, label := range mtr.GetLabel() {
			       if label.GetName() == "id" && label.GetValue() == peerId {
				       found = true
			       }
		       }
		       if found {
			       // Restore label values in correct order
			       for _, l := range peerLabels {
				       val := ""
				       for _, label := range mtr.GetLabel() {
					       if label.GetName() == l {
						       val = label.GetValue()
						       break
					       }
				       }
				       labelValues = append(labelValues, val)
			       }
			       vec.DeleteLabelValues(labelValues...)
		       }
	       }
       }
}

// Remove all peer metrics by id, regardless of other label values
func (m *MetricsServer) RemovePeerMetrics(peer *domain.Peer) {
       if peer == nil {
	       slog.Warn("Attempted to remove metrics for a nil peer")
	       return
       }

       peerId := string(peer.Identifier)
       slog.Debug("Starting removal of metrics for peer by id", "id", peerId, "name", peer.DisplayName)

       mfs, err := m.registry.Gather()
       if err != nil {
	       slog.Warn("Failed to gather metrics for removal", "err", err)
	       return
       }

       metricMap := map[string]*prometheus.GaugeVec{
	       "wireguard_peer_up": m.peerIsConnected,
       }

       if m.cfg.Statistics.ExportDetailedPeerMetrics {
	       metricMap["wireguard_peer_last_handshake_seconds"] = m.peerLastHandshakeSeconds
	       metricMap["wireguard_peer_received_bytes_total"] = m.peerReceivedBytesTotal
	       metricMap["wireguard_peer_sent_bytes_total"] = m.peerSendBytesTotal
       }

       for _, mf := range mfs {
	       name := mf.GetName()
	       vec, ok := metricMap[name]
	       if !ok {
		       continue
	       }
	       for _, mtr := range mf.GetMetric() {
		       var labelValues []string
		       var found bool
		       for _, label := range mtr.GetLabel() {
			       if label.GetName() == "id" && label.GetValue() == peerId {
				       found = true
			       }
		       }
		       if found {
			       for _, l := range peerLabels {
				       val := ""
				       for _, label := range mtr.GetLabel() {
					       if label.GetName() == l {
						       val = label.GetValue()
						       break
					       }
				       }
				       labelValues = append(labelValues, val)
			       }
			       vec.DeleteLabelValues(labelValues...)
			       slog.Debug("Removed metric by id", "metric", name, "id", peerId, "labels", labelValues)
		       }
	       }
       }

       slog.Info("Completed removal of metrics for peer by id", "id", peerId, "name", peer.DisplayName)
}

// Remove all peer metrics by id only (for when peer object is no longer available)
func (m *MetricsServer) RemovePeerMetricsByID(peerId string) {
       slog.Debug("Starting removal of metrics for peer by id", "id", peerId, "name", "unknown")

       mfs, err := m.registry.Gather()
       if err != nil {
	       slog.Warn("Failed to gather metrics for removal", "err", err)
	       return
       }

       metricMap := map[string]*prometheus.GaugeVec{
	       "wireguard_peer_up": m.peerIsConnected,
       }

       if m.cfg.Statistics.ExportDetailedPeerMetrics {
	       metricMap["wireguard_peer_last_handshake_seconds"] = m.peerLastHandshakeSeconds
	       metricMap["wireguard_peer_received_bytes_total"] = m.peerReceivedBytesTotal
	       metricMap["wireguard_peer_sent_bytes_total"] = m.peerSendBytesTotal
       }

       for _, mf := range mfs {
	       name := mf.GetName()
	       vec, ok := metricMap[name]
	       if !ok {
		       continue
	       }
	       for _, mtr := range mf.GetMetric() {
		       var labelValues []string
		       var found bool
		       for _, label := range mtr.GetLabel() {
			       if label.GetName() == "id" && label.GetValue() == peerId {
				       found = true
			       }
		       }
		       if found {
			       for _, l := range peerLabels {
				       val := ""
				       for _, label := range mtr.GetLabel() {
					       if label.GetName() == l {
						       val = label.GetValue()
						       break
					       }
				       }
				       labelValues = append(labelValues, val)
			       }
			       vec.DeleteLabelValues(labelValues...)
			       slog.Debug("Removed metric by id", "metric", name, "id", peerId, "labels", labelValues)
		       }
	       }
       }

       slog.Info("Completed removal of metrics for peer by id", "id", peerId, "name", "unknown")
}
