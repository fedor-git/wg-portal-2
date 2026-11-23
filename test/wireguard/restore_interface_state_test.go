package wireguard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fedor-git/wg-portal-2/internal/app/wireguard"
	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

type restoreTestDb struct {
	wireguard.InterfaceAndPeerDatabaseRepo
	iface domain.Interface
}

func (d *restoreTestDb) GetAllInterfaces(context.Context) ([]domain.Interface, error) {
	return []domain.Interface{d.iface}, nil
}

func (d *restoreTestDb) GetInterface(context.Context, domain.InterfaceIdentifier) (*domain.Interface, error) {
	in := d.iface
	return &in, nil
}

func (d *restoreTestDb) GetInterfacePeers(context.Context, domain.InterfaceIdentifier) ([]domain.Peer, error) {
	return nil, nil
}

func (d *restoreTestDb) SaveInterface(
	_ context.Context,
	_ domain.InterfaceIdentifier,
	updateFunc func(in *domain.Interface) (*domain.Interface, error),
) error {
	in := d.iface
	out, err := updateFunc(&in)
	if err != nil {
		return err // rolled back
	}
	d.iface = *out
	return nil
}

type restoreTestController struct {
	wireguard.InterfaceController
	exists  bool
	saveErr error
}

func (c *restoreTestController) GetInterface(
	_ context.Context,
	id domain.InterfaceIdentifier,
) (*domain.PhysicalInterface, error) {
	if !c.exists {
		return nil, errors.New("interface not found")
	}
	return &domain.PhysicalInterface{Identifier: id}, nil
}

func (c *restoreTestController) SaveInterface(
	_ context.Context,
	id domain.InterfaceIdentifier,
	updateFunc func(pi *domain.PhysicalInterface) (*domain.PhysicalInterface, error),
) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	_, err := updateFunc(&domain.PhysicalInterface{Identifier: id})
	c.exists = err == nil
	return err
}

func (c *restoreTestController) GetPeers(context.Context, domain.InterfaceIdentifier) ([]domain.PhysicalPeer, error) {
	return nil, nil
}

type restoreTestQuick struct {
	hooks []string
}

func (q *restoreTestQuick) ExecuteInterfaceHook(_ domain.InterfaceIdentifier, hookCmd string) error {
	q.hooks = append(q.hooks, hookCmd)
	return nil
}
func (q *restoreTestQuick) SetDNS(domain.InterfaceIdentifier, string, string) error { return nil }
func (q *restoreTestQuick) UnsetDNS(domain.InterfaceIdentifier) error               { return nil }

type restoreTestBus struct{}

func (restoreTestBus) Publish(string, ...any)              {}
func (restoreTestBus) Subscribe(string, interface{}) error { return nil }

func newRestoreTestManager(
	t *testing.T,
	iface domain.Interface,
	ctrl *restoreTestController,
) (*wireguard.Manager, *restoreTestDb, *restoreTestQuick) {
	t.Helper()

	oldInitial, oldMax := wireguard.RestoreRetryInitialDelay, wireguard.RestoreRetryMaxDelay
	wireguard.RestoreRetryInitialDelay, wireguard.RestoreRetryMaxDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { wireguard.RestoreRetryInitialDelay, wireguard.RestoreRetryMaxDelay = oldInitial, oldMax })

	db := &restoreTestDb{iface: iface}
	quick := &restoreTestQuick{}
	cfg := &config.Config{}
	controllers := wireguard.NewControllerManagerWithControllers(cfg,
		map[domain.InterfaceBackend]wireguard.InterfaceController{config.LocalBackendName: ctrl})
	m, err := wireguard.NewWireGuardManager(cfg, restoreTestBus{}, controllers, quick, db, nil)
	require.NoError(t, err)
	return m, db, quick
}

func adminCtx() context.Context {
	return domain.SetUserInfo(context.Background(), domain.SystemAdminContextUserInfo())
}

func TestRestoreInterfaceState_MissingInterfaceIsRecreatedWithUpHooks(t *testing.T) {
	iface := domain.Interface{Identifier: "wg0", PreUp: "pre-up", PostUp: "post-up"}
	m, db, quick := newRestoreTestManager(t, iface, &restoreTestController{exists: false})

	require.NoError(t, m.RestoreInterfaceState(adminCtx()))

	assert.False(t, db.iface.IsDisabled())
	assert.Equal(t, []string{"pre-up", "post-up"}, quick.hooks)
}

func TestRestoreInterfaceState_FailedRecreateKeepsInterfaceEnabledInDb(t *testing.T) {
	iface := domain.Interface{Identifier: "wg0"}
	m, db, _ := newRestoreTestManager(t, iface,
		&restoreTestController{exists: false, saveErr: errors.New("wireguard module not loaded")})

	require.Error(t, m.RestoreInterfaceState(adminCtx()))

	assert.False(t, db.iface.IsDisabled())
	assert.Empty(t, db.iface.DisabledReason)
}

func TestRestoreInterfaceState_ReEnablesInterfaceAutoDisabledAsMissing(t *testing.T) {
	disabledAt := time.Now()
	iface := domain.Interface{
		Identifier:     "wg0",
		Disabled:       &disabledAt,
		DisabledReason: domain.DisabledReasonInterfaceMissing,
	}
	m, db, _ := newRestoreTestManager(t, iface, &restoreTestController{exists: false})

	require.NoError(t, m.RestoreInterfaceState(adminCtx()))

	assert.False(t, db.iface.IsDisabled())
	assert.Empty(t, db.iface.DisabledReason)
}

func TestRestoreInterfaceState_KeepsInterfaceDisabledByUser(t *testing.T) {
	disabledAt := time.Now()
	iface := domain.Interface{Identifier: "wg0", Disabled: &disabledAt, DisabledReason: "admin"}
	m, db, _ := newRestoreTestManager(t, iface, &restoreTestController{exists: true})

	require.NoError(t, m.RestoreInterfaceState(adminCtx()))

	assert.True(t, db.iface.IsDisabled())
	assert.Equal(t, "admin", db.iface.DisabledReason)
}
