# WireGuard Portal Statistics Module

## Overview

The statistics module (`statistics.go`) is a comprehensive monitoring and metrics collection system for WireGuard Portal. It handles real-time data collection, peer monitoring, ping checks, and metrics reporting to monitoring systems like Prometheus.

## Architecture

### Core Components

#### StatisticsCollector
The main orchestrator that manages all statistics collection operations:
- **Peer Monitoring**: Tracks peer status, connection state, and traffic
- **Interface Monitoring**: Collects interface-level statistics
- **Ping Checks**: Validates peer connectivity with configurable workers
- **Metrics Reporting**: Updates Prometheus metrics in real-time
- **Event Handling**: Responds to peer/interface events via message bus

### Key Interfaces

#### StatisticsDatabaseRepo
Defines database operations for statistics persistence:
```go
- GetAllInterfaces() - Fetch all WireGuard interfaces
- GetInterfacePeers() - Get peers for specific interface
- GetPeer() - Retrieve individual peer data
- UpdatePeerStatus() - Update peer connection status
- UpdateInterfaceStatus() - Update interface statistics
```

#### StatisticsMetricsServer
Interface for metrics system integration (Prometheus):
```go
- UpdateInterfaceMetrics() - Push interface metrics
- UpdatePeerMetrics() - Push peer metrics
- RemovePeerMetrics() - Clean up peer metrics
```

#### StatisticsEventBus
Event-driven communication system:
```go
- Subscribe() - Listen to specific topics
- Publish() - Broadcast events to subscribers
```

## Key Features

### 1. Intelligent Peer Ping System

#### Multi-Worker Architecture
- **Configurable Workers**: Default 10 parallel ping workers
- **Job Queue**: Buffered channel for ping tasks
- **Smart Scheduling**: Configurable intervals (default: 1 minute)

#### Address Resolution Logic
```go
func getPeerPingAddress(peer domain.Peer) string
```
- **Priority 1**: Uses `CheckAliveAddress` if explicitly set
- **Priority 2**: Extracts first IP from `AllowedIPs`
- **CIDR Handling**: Automatically strips `/32`, `/24` suffixes
- **Validation**: Returns empty string if no valid address found

#### Ping Success Tracking
- **Initial State**: All peers start with `peerPingSuccess = false`
- **Address Hiding**: Metrics show empty address until first successful ping
- **State Persistence**: Successful ping status maintained in cache

### 2. Performance Optimizations

#### Peer Cache System
```go
peerCache map[domain.PeerIdentifier]*domain.Peer
```
- **Cache-First**: Metrics updates check cache before database
- **Preservation Logic**: Maintains peer data during updates
- **Fallback Mechanism**: Database lookup if cache miss

#### Metrics Label Caching
```go
metricsLabels map[domain.PeerIdentifier][]string
```
- **Label Storage**: Caches Prometheus labels per peer
- **Cleanup Integration**: Orphan metrics removal
- **Performance**: Reduces label regeneration overhead

### 3. Real-Time Event Integration

#### Message Bus Subscriptions
- **`TopicPeerCreated`**: Instant metrics for new peers
- **`TopicPeerUpdated`**: Real-time peer changes
- **`TopicPeerIdentifierUpdated`**: Handle peer ID migrations

#### Fanout Synchronization
- **Cluster Support**: Multi-node synchronization
- **Event Propagation**: Cross-node peer updates
- **Instant Metrics**: Immediate metrics creation on peer events

### 4. Data Collection Workers

#### Interface Data Fetcher
- **Periodic Collection**: Configurable intervals
- **Traffic Statistics**: Bytes uploaded/downloaded
- **Status Updates**: Real-time interface status

#### Peer Data Fetcher
- **Connection Tracking**: Handshake monitoring
- **Session Management**: Session start/restart detection
- **Traffic Analysis**: Per-peer traffic statistics
- **State Changes**: Connection state event publishing

## Configuration

### Statistics Settings
```yaml
statistics:
  use_ping_checks: true           # Enable/disable ping system
  ping_check_workers: 20          # Number of parallel ping workers
  ping_check_interval: 90s        # Ping check frequency
  data_collection_interval: 60s   # Statistics collection interval
  collect_interface_data: true    # Interface monitoring
  collect_peer_data: true         # Peer monitoring
  listening_address: ":8787"      # Metrics server endpoint
```

### Performance Tuning for Large Deployments

#### 200+ Peers Optimization
- **Workers**: Increase to 20+ for faster ping processing
- **Intervals**: Use 90s+ to reduce system load
- **Batching**: Automatic job queuing and processing

## Key Methods

### Lifecycle Management

#### `NewStatisticsCollector()`
- Creates and initializes collector
- Sets up caches and event subscriptions
- Connects to message bus

#### `StartBackgroundJobs(ctx)`
- **Non-blocking**: Returns immediately
- **Worker Startup**: Ping workers, data fetchers
- **Initial Population**: Metrics for existing peers
- **Graceful Shutdown**: Context-based cleanup

### Metrics Management

#### `EnsurePeerMetrics(ctx)`
- **Database Sync**: Ensures metrics for all DB peers
- **Cache Population**: Builds peer cache
- **Initial State**: Sets up empty addresses for new peers
- **Idempotent**: Safe to call multiple times

#### `updatePeerMetrics(ctx, status)`
- **Cache-First**: Performance optimization
- **Address Logic**: Shows address only after successful ping
- **Label Management**: Updates cached Prometheus labels
- **Fallback**: Database lookup for cache misses

### Ping System

#### `enqueuePingChecks(ctx)`
- **Periodic Scheduling**: Ticker-based job creation
- **Interface Iteration**: Processes all interfaces
- **Job Creation**: Enqueues ping tasks for all peers
- **Overflow Protection**: Handles full job queues

#### `pingWorker(ctx)`
- **Job Processing**: Consumes ping jobs from queue
- **Status Updates**: Updates peer ping status in database
- **Success Tracking**: Marks successful pings for address display
- **Event Publishing**: Connection state change events

### Cleanup Operations

#### `CleanOrphanPeerMetrics(ctx)`
- **Database Sync**: Compares metrics to database peers
- **Orphan Removal**: Cleans up metrics for deleted peers
- **Cache Cleanup**: Removes stale cache entries
- **Label Cleanup**: Removes cached labels

#### `RemovePeerMetrics(peerID)`
- **Database Lookup**: Attempts to fetch peer data
- **Graceful Handling**: Handles missing peer records
- **Cache Fallback**: Uses cached labels if DB lookup fails
- **Complete Cleanup**: Removes from metrics and caches

## Event Handling

### Peer Lifecycle Events

#### Peer Creation (`handlePeerCreatedEvent`)
1. **Cache Update**: Adds peer to cache
2. **Status Initialization**: Creates default peer status
3. **Metrics Creation**: Instant metrics for new peer
4. **Ping Setup**: Initializes ping success tracking

#### Peer Updates
- **Real-time Processing**: Immediate cache/metrics updates
- **Fanout Integration**: Cross-node synchronization
- **State Preservation**: Maintains ping success state

## Error Handling

### Graceful Degradation
- **Database Failures**: Continue with cached data
- **Ping Failures**: Log and continue processing
- **Metrics Errors**: Non-blocking operations

### Logging Strategy
- **Debug Level**: Detailed ping and cache operations
- **Info Level**: Successful pings and major events
- **Warn Level**: Non-critical failures
- **Error Level**: Critical system issues

## Performance Characteristics

### Memory Usage
- **Peer Cache**: ~1KB per peer (estimated)
- **Labels Cache**: ~200 bytes per peer
- **Ping Success Map**: ~50 bytes per peer

### CPU Usage
- **Ping Workers**: Parallel processing reduces CPU spikes
- **Cache Hits**: Significant reduction in database queries
- **Event Processing**: Minimal overhead for real-time updates

### Network Impact
- **Ping Traffic**: 1 packet per peer per interval
- **Database Queries**: Reduced by caching
- **Metrics Updates**: Batched when possible

## Troubleshooting

### Common Issues

#### High Ping Times
- **Solution**: Increase `ping_check_workers`
- **Monitoring**: Check `enqueuePingChecks` logs
- **Optimization**: Adjust `ping_check_interval`

#### Missing Metrics
- **Diagnosis**: Check `EnsurePeerMetrics` logs
- **Solution**: Verify database connectivity
- **Cache Issue**: Clear cache and restart

#### Memory Growth
- **Cause**: Orphaned peer data
- **Solution**: `CleanOrphanPeerMetrics` runs automatically
- **Monitoring**: Track cache sizes

### Debug Information

#### Key Log Messages
```
[EnsurePeerMetrics] peers to process: count=X
[pingWorker] peer ping successful - address will now be shown
[collectPeerData] preserving existing peer cache
[enqueuePingChecks] enqueued ping jobs: total=X
```

#### Metrics Monitoring
- `wg_peer_ping_success`: Ping success rate
- `wg_peer_connection_state`: Connection status
- `wg_interface_bytes_total`: Traffic statistics

## Version History

### Recent Changes

#### Ping System Improvements
- ✅ **Address Hiding**: Empty address until successful ping
- ✅ **Cache Performance**: Reduced database queries
- ✅ **Multi-Worker**: Parallel ping processing
- ✅ **Success Tracking**: Per-peer ping state

#### Fanout Integration
- ✅ **Real-time Sync**: Cross-node peer synchronization
- ✅ **Event Handling**: Instant metrics creation
- ✅ **V0 API Fix**: Added missing fanout events

#### Memory Optimizations
- ✅ **Peer Caching**: Cache-first metrics updates
- ✅ **Label Caching**: Reduced label regeneration
- ✅ **Orphan Cleanup**: Automatic stale data removal

#### Documentation
- ✅ **Code Comments**: Comprehensive English documentation
- ✅ **Function Descriptions**: Clear purpose statements
- ✅ **Type Documentation**: Interface and struct descriptions

## Future Considerations

### Scalability Improvements
- **Database Pooling**: For high-peer deployments
- **Metrics Batching**: Reduce Prometheus load
- **Cache Warming**: Pre-populate on startup

### Feature Enhancements
- **Health Checks**: Beyond simple ping
- **Custom Metrics**: User-defined monitoring
- **Alert Integration**: Threshold-based notifications

### Performance Monitoring
- **Metrics Dashboard**: Statistics system health
- **Performance Profiling**: Memory and CPU analysis
- **Benchmark Testing**: Large-scale deployment validation