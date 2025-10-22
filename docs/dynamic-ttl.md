# Peer DateTime & Dynamic TTL Fixes – Summary

## Overview

This update resolves critical issues with peer expiration datetime handling and implements robust dynamic TTL (Time To Live) management for WireGuard peers in the wg-portal-2 project. The changes ensure full RFC3339 timestamp support, compatibility between frontend and backend, and dynamic peer expiration based on connection state.

---

## Problems Addressed

- **Failed to save peer!** error due to datetime parsing issues between frontend and backend.
- Frontend sent RFC3339 timestamps (e.g., `"2025-10-23T20:22:00.000Z"`), but backend v0 API expected legacy `"2006-01-02"` format.
- Double-quoted JSON values from Vue 3 reactive proxies caused additional parsing errors.
- Dynamic TTL was not being applied to connected/disconnected peers as expected.

---

## Key Fixes & Improvements

### 1. RFC3339 DateTime Support (Backend)

- **v0 API Model Update:**  
  - `ExpiryDate.UnmarshalJSON()` and `MarshalJSON()` now robustly handle RFC3339, RFC3339 with milliseconds, and legacy formats.
  - Double-quote trimming logic added to support Vue/reactive serialization quirks.
  - Backward compatibility with old date-only format retained.

- **v1 API Model Update:**  
  - Removed strict binding validation for `ExpiresAt` field.
  - Improved parsing logic to trim quotes and support multiple datetime formats.
  - Added detailed debug logging for received datetime values.

### 2. Frontend Fixes

- **Datetime Serialization:**  
  - Ensured `formatDateTimeToRFC3339()` always returns a plain string, not a Vue proxy.
  - Explicit `String()` conversion before sending to API to prevent double-quoting.
  - Added debug logging for all datetime operations.

- **User Experience:**  
  - Peer creation and editing now work with precise datetime values.
  - No more "Failed to save peer!" errors due to format mismatches.

### 3. Dynamic TTL Management

- **Event-Driven TTL:**  
  - On peer connect/disconnect, the backend updates `ExpiresAt` to `now + DefaultUserTTL`.
  - Handles both connection and disconnection events for accurate TTL assignment.
  - TTL is initialized for all peers on startup if missing or expired.

- **Configuration:**  
  - `core.default_user_ttl` in config supports flexible formats: `1h`, `24h`, `2d`, etc.
  - RFC3339 with timezone is now the standard for all expiration fields.

- **Permissions Fix:**  
  - All TTL updates and peer modifications now use system admin context to avoid permission errors.

### 4. Testing & Validation

- Comprehensive unit tests for all datetime parsing edge cases (RFC3339, legacy, double quotes, timezones, invalid formats).
- Manual and automated tests for peer creation, editing, and dynamic TTL assignment.
- Docker image rebuilt and containers restarted to ensure all changes are live.

---

## Benefits

- **Full RFC3339 support** for all peer expiration fields.
- **No more frontend/backend datetime mismatches** or double-quote errors.
- **Dynamic, event-driven TTL** for peers based on real connection state.
- **Backward compatibility** with legacy data and APIs.
- **Robust logging and test coverage** for easier debugging and maintenance.

---

## Files Modified

- `internal/app/api/v0/model/models_peer.go`
- `internal/app/api/v1/models/models_peer.go`
- `internal/app/api/v1/handlers/endpoint_peer.go`
- `frontend/src/components/PeerEditModal.vue`
- `frontend/src/components/UserPeerEditModal.vue`
- `frontend/src/helpers/datetime.js`
- `frontend/src/helpers/fetch-wrapper.js`
- `internal/app/wireguard/wireguard.go`
- `docs/dynamic-ttl-management.md`
- `docs/v0-api-datetime-fix.md`
- `docs/frontend-double-quotes-fix.md`
- `docs/frontend-datetime-fix.md`
- `docs/peer-datetime-fix-summary.md`

---

## How to Use

1. Set `core.default_user_ttl` in your config (e.g., `"24h"`).
2. Use the web interface to create or edit peers with precise expiration.
3. Peer expiration is now managed dynamically based on connection state.
4. All datetime values are stored and displayed in RFC3339 format.

---

## References

- See `docs/dynamic-ttl-management.md` for full TTL logic and configuration.
- See `docs/v0-api-datetime-fix.md` and `docs/frontend-double-quotes-fix.md` for technical details on datetime fixes.

---

**Result:**  
Peer save and update now work reliably, dynamic TTL is applied as expected, and all datetime fields are robustly handled across the stack.  
**The "Failed to save peer!" error is fully resolved.** ✅
