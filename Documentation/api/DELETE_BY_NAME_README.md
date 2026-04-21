# Batch Delete Peers by DisplayName

Quick reference for the `DELETE /api/v1/peer/by-name` endpoint.

## Usage

Delete multiple WireGuard peers by their display name in one batch request:

```bash
curl -X DELETE "http://localhost:8080/api/v1/peer/by-name" \
  -H "Content-Type: application/json" \
  -u "admin:password" \
  -d '[
    {"displayName": "user1"},
    {"displayName": "user2"},
    {"displayName": "user3"}
  ]'
```

## Request

- **URL**: `/api/v1/peer/by-name`
- **Method**: `DELETE`
- **Auth**: Admin only
- **Body**: Array of objects with `displayName` field (case-sensitive, exact match)

Supported key variants:
- `displayName` (camelCase)
- `DisplayName` (PascalCase)
- `display_name` (snake_case)

## Response

```json
[
  {"displayName": "user1", "status": "Deleted"},
  {"displayName": "user2", "status": "Not found"},
  {"displayName": "user3", "status": "Failed"}
]
```

**Status values**:
- `Deleted` - Successfully deleted
- `Not found` - No peers with this name
- `Invalid` - Missing/empty displayName
- `Failed` - Peers found but deletion failed

## Features

✅ Batch delete multiple peers at once  
✅ Direct database query (O(1) lookup)  
✅ Automatic sync to cluster nodes  
✅ Detailed per-peer status response  
✅ Admin-only access  

## Implementation

- **Handler**: `internal/app/api/v1/handlers/endpoint_peer.go` - `handleDeleteByName()`
- **Service**: `internal/app/api/v1/backend/peer_service.go` - `GetByDisplayName()`
- **Database**: `internal/adapters/database.go` - `GetPeersByDisplayName()`
- **Manager**: `internal/app/wireguard/wireguard_peers.go` - `GetPeersByDisplayName()`

## Sync to Other Nodes

Each peer deletion automatically publishes `TopicPeerDeletedSync` event, triggering:
- Event-driven sync to cluster nodes
- Automatic peer removal on remote nodes
- Full synchronization without polling

## Examples

### Single peer
```bash
curl -X DELETE "http://localhost:8080/api/v1/peer/by-name" \
  -H "Content-Type: application/json" \
  -u "admin:password" \
  -d '[{"displayName":"adm1"}]'
```

Response:
```json
[{"displayName": "adm1", "status": "Deleted"}]
```

### Error handling
Empty body:
```json
{"code": 400, "message": "request body cannot be empty"}
```

Invalid JSON:
```json
{"code": 400, "message": "invalid request body: ..."}
```

## Performance

- Direct indexed SQL query: `WHERE display_name = ?`
- One query per display name
- No in-memory filtering
- Suitable for large-scale operations
