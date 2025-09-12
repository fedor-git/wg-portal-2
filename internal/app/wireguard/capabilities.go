package wireguard

import "context"

type SupportsClearPeers interface {
    ClearPeers(ctx context.Context, iface string) error
}