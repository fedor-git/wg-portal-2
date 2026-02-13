package wireguard

import (
	"context"
	"fmt"

	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

func (c *ControllerManager) ClearPeers(ctx context.Context, iface string) error {
	iid := domain.InterfaceIdentifier(iface)

	for _, inst := range c.controllers {
		if _, err := inst.Implementation.GetInterface(ctx, iid); err == nil {
			return inst.Implementation.ClearPeers(ctx, iface)
		}
	}

	if inst, ok := c.controllers[config.LocalBackendName]; ok {
		return inst.Implementation.ClearPeers(ctx, iface)
	}
	return fmt.Errorf("ClearPeers: controller for %s not found", iface)
}
