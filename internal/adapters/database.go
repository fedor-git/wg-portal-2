package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils"

	"github.com/fedor-git/wg-portal-2/internal/config"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

// SchemaVersion describes the current database schema version. It must be incremented if a manual migration is needed.
var SchemaVersion uint64 = 1

// SysStat stores the current database schema version and the timestamp when it was applied.
type SysStat struct {
	MigratedAt    time.Time `gorm:"column:migrated_at"`
	SchemaVersion uint64    `gorm:"primaryKey,column:schema_version"`
}

// GormLogger is a custom logger for Gorm, making it use slog
type GormLogger struct {
	SlowThreshold           time.Duration
	SourceField             string
	IgnoreErrRecordNotFound bool
	Debug                   bool
	Silent                  bool

	prefix string
}

func NewLogger(slowThreshold time.Duration, debug bool) *GormLogger {
	return &GormLogger{
		SlowThreshold:           slowThreshold,
		Debug:                   debug,
		IgnoreErrRecordNotFound: true,
		Silent:                  false,
		SourceField:             "src",
		prefix:                  "GORM-SQL: ",
	}
}

func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	if level == logger.Silent {
		l.Silent = true
	} else {
		l.Silent = false
	}
	return l
}

func (l *GormLogger) Info(ctx context.Context, s string, args ...any) {
	if l.Silent {
		return
	}
	slog.InfoContext(ctx, l.prefix+s, args...)
}

func (l *GormLogger) Warn(ctx context.Context, s string, args ...any) {
	if l.Silent {
		return
	}
	slog.WarnContext(ctx, l.prefix+s, args...)
}

func (l *GormLogger) Error(ctx context.Context, s string, args ...any) {
	if l.Silent {
		return
	}
	slog.ErrorContext(ctx, l.prefix+s, args...)
}

func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()

	attrs := []any{
		"rows", rows,
		"duration", elapsed,
	}

	if l.SourceField != "" {
		attrs = append(attrs, l.SourceField, utils.FileWithLineNum())
	}

	if err != nil && !(errors.Is(err, gorm.ErrRecordNotFound) && l.IgnoreErrRecordNotFound) {
		attrs = append(attrs, "error", err)
		slog.ErrorContext(ctx, l.prefix+sql, attrs...)
		return
	}

	if l.SlowThreshold != 0 && elapsed > l.SlowThreshold {
		slog.WarnContext(ctx, l.prefix+sql, attrs...)
		return
	}

	if l.Debug {
		slog.DebugContext(ctx, l.prefix+sql, attrs...)
	}
}

// applyConnectionPoolConfig applies the connection pool configuration from config to the database.
func applyConnectionPoolConfig(db *gorm.DB, cfg config.DatabaseConfig) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// Use configured values, or defaults if not specified
	maxOpen := cfg.MaxOpenConnections
	if maxOpen <= 0 {
		maxOpen = 50
	}

	maxIdle := cfg.MaxIdleConnections
	if maxIdle <= 0 {
		maxIdle = 10
	}

	maxLifetime := cfg.ConnectionMaxLifetime
	if maxLifetime <= 0 {
		maxLifetime = 3 * time.Minute
	}

	// Don't open connections until needed - reduces startup pool exhaustion
	// In multi-node cluster with 24 nodes, early connection creation causes pool exhaustion
	maxIdle = 5 // Start with fewer idle connections
	
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(maxLifetime)
	// Set connection max idle time to recycle stale connections faster
	// Prevents "too many connections" by recycling idle connections after 5 minutes
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	slog.Info("Database connection pool configured",
		"max_open_connections", maxOpen,
		"max_idle_connections", maxIdle,
		"connection_max_lifetime", maxLifetime.String(),
		"connection_max_idle_time", "5m")

	return nil
}

// NewDatabase creates a new database connection and returns a Gorm database instance.
func NewDatabase(cfg config.DatabaseConfig) (*gorm.DB, error) {
	var gormDb *gorm.DB
	var err error

	// Retry logic for initial database connection
	// With 24 nodes starting simultaneously, initial connection can fail with "Too many connections"
	const maxRetries = 10
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with random jitter to prevent thundering herd
			// With 24 nodes starting simultaneously, stagger retries
			// Exponential: 100ms, 200ms, 400ms, 800ms, 1.6s, 3.2s...
			// Jitter: ±50% randomness so not all nodes retry at same time
			baseWaitTime := time.Duration(100*(1<<uint(attempt-1))) * time.Millisecond
			jitterAmount := time.Duration(rand.Intn(int(baseWaitTime/2))) // ±50% jitter
			waitTime := baseWaitTime + jitterAmount
			
			slog.Warn("database connection attempt failed, retrying",
				"attempt", attempt+1,
				"max_retries", maxRetries,
				"wait_time", waitTime.String(),
				"error", lastErr)
			time.Sleep(waitTime)
		}

		switch cfg.Type {
		case config.DatabaseMySQL:
			gormDb, err = gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
				Logger: NewLogger(cfg.SlowQueryThreshold, cfg.Debug),
			})
			if err != nil {
				lastErr = fmt.Errorf("failed to open MySQL database: %w", err)
				if strings.Contains(err.Error(), "Too many connections") {
					continue // Retry on connection pool exhaustion
				}
				return nil, lastErr
			}

			// Apply connection pool configuration
			if err := applyConnectionPoolConfig(gormDb, cfg); err != nil {
				lastErr = fmt.Errorf("failed to configure MySQL connection pool: %w", err)
				continue
			}

			sqlDB, _ := gormDb.DB()
			err = sqlDB.Ping() // This DOES open a connection if necessary
			if err != nil {
				lastErr = fmt.Errorf("failed to ping MySQL database: %w", err)
				if strings.Contains(err.Error(), "Too many connections") {
					continue // Retry on connection pool exhaustion
				}
				return nil, lastErr
			}

			slog.Info("database connected successfully", "type", "MySQL", "attempt", attempt+1)
			return gormDb, nil

		case config.DatabaseMsSQL:
			gormDb, err = gorm.Open(sqlserver.Open(cfg.DSN), &gorm.Config{
				Logger: NewLogger(cfg.SlowQueryThreshold, cfg.Debug),
			})
			if err != nil {
				lastErr = fmt.Errorf("failed to open sqlserver database: %w", err)
				if strings.Contains(err.Error(), "Too many connections") {
					continue
				}
				return nil, lastErr
			}

			// Apply connection pool configuration
			if err := applyConnectionPoolConfig(gormDb, cfg); err != nil {
				lastErr = fmt.Errorf("failed to configure MSSQL connection pool: %w", err)
				continue
			}

			slog.Info("database connected successfully", "type", "MSSQL", "attempt", attempt+1)
			return gormDb, nil

		case config.DatabasePostgres:
			gormDb, err = gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
				Logger: NewLogger(cfg.SlowQueryThreshold, cfg.Debug),
			})
			if err != nil {
				lastErr = fmt.Errorf("failed to open Postgres database: %w", err)
				if strings.Contains(err.Error(), "too many connections") {
					continue
				}
				return nil, lastErr
			}

			// Apply connection pool configuration
			if err := applyConnectionPoolConfig(gormDb, cfg); err != nil {
				lastErr = fmt.Errorf("failed to configure Postgres connection pool: %w", err)
				continue
			}

			slog.Info("database connected successfully", "type", "Postgres", "attempt", attempt+1)
			return gormDb, nil

		case config.DatabaseSQLite:
			gormDb, err = gorm.Open(sqlite.Open(cfg.DSN), &gorm.Config{
				Logger: NewLogger(cfg.SlowQueryThreshold, cfg.Debug),
			})
			if err != nil {
				lastErr = fmt.Errorf("failed to open SQLite database: %w", err)
				continue
			}

			// SQLite doesn't benefit from connection pooling, set to 1
			if err := applyConnectionPoolConfig(gormDb, config.DatabaseConfig{
				MaxOpenConnections:    1,
				MaxIdleConnections:    1,
				ConnectionMaxLifetime: 0,
			}); err != nil {
				lastErr = fmt.Errorf("failed to configure SQLite connection pool: %w", err)
				continue
			}

			slog.Info("database connected successfully", "type", "SQLite", "attempt", attempt+1)
			return gormDb, nil

		default:
			return nil, fmt.Errorf("unknown database type: %s", cfg.Type)
		}
	}

	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", maxRetries, lastErr)
}

// SqlRepo is a SQL database repository implementation.
// Currently, it supports MySQL, SQLite, Microsoft SQL and Postgresql database systems.
type SqlRepo struct {
	db *gorm.DB
}

// NewSqlRepository creates a new SqlRepo instance.
func NewSqlRepository(db *gorm.DB) (*SqlRepo, error) {
	repo := &SqlRepo{
		db: db,
	}

	if err := repo.preCheck(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	if err := repo.migrate(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return repo, nil
}

func (r *SqlRepo) preCheck() error {
	// WireGuard Portal v1 database migration table
	type DatabaseMigrationInfo struct {
		Version string `gorm:"primaryKey"`
		Applied time.Time
	}

	// temporarily disable logger as the next request might fail (intentionally)
	r.db.Logger.LogMode(logger.Silent)
	defer func() { r.db.Logger.LogMode(logger.Info) }()

	lastVersion := DatabaseMigrationInfo{}
	err := r.db.Order("applied desc, version desc").FirstOrInit(&lastVersion).Error
	if err != nil {
		return nil // we probably don't have a V1 database =)
	}

	return fmt.Errorf("detected a WireGuard Portal V1 database (version: %s) - please migrate first",
		lastVersion.Version)
}

func (r *SqlRepo) migrate() error {
	slog.Debug("running migration: sys-stat", "result", r.db.AutoMigrate(&SysStat{}))
	slog.Debug("running migration: user", "result", r.db.AutoMigrate(&domain.User{}))
	slog.Debug("running migration: user webauthn credentials", "result",
		r.db.AutoMigrate(&domain.UserWebauthnCredential{}))
	slog.Debug("running migration: interface", "result", r.db.AutoMigrate(&domain.Interface{}))
	slog.Debug("running migration: peer", "result", r.db.AutoMigrate(&domain.Peer{}))
	slog.Debug("running migration: peer status", "result", r.db.AutoMigrate(&domain.PeerStatus{}))
	slog.Debug("running migration: interface status", "result", r.db.AutoMigrate(&domain.InterfaceStatus{}))
	slog.Debug("running migration: audit data", "result", r.db.AutoMigrate(&domain.AuditEntry{}))

	existingSysStat := SysStat{}
	r.db.Where("schema_version = ?", SchemaVersion).First(&existingSysStat)
	if existingSysStat.SchemaVersion == 0 {
		sysStat := SysStat{
			MigratedAt:    time.Now(),
			SchemaVersion: SchemaVersion,
		}
		if err := r.db.Create(&sysStat).Error; err != nil {
			return fmt.Errorf("failed to write sysstat entry for schema version %d: %w", SchemaVersion, err)
		}
		slog.Debug("sys-stat entry written", "schema_version", SchemaVersion)
	}

	return nil
}

// region interfaces

// GetInterface returns the interface with the given id.
// If no interface is found, an error domain.ErrNotFound is returned.
func (r *SqlRepo) GetInterface(ctx context.Context, id domain.InterfaceIdentifier) (*domain.Interface, error) {
	var in domain.Interface

	err := r.db.WithContext(ctx).Preload("Addresses").First(&in, id).Error

	if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &in, nil
}

// GetInterfaceAndPeers returns the interface with the given id and all peers associated with it.
// If no interface is found, an error domain.ErrNotFound is returned.
func (r *SqlRepo) GetInterfaceAndPeers(ctx context.Context, id domain.InterfaceIdentifier) (
	*domain.Interface,
	[]domain.Peer,
	error,
) {
	in, err := r.GetInterface(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load interface: %w", err)
	}

	peers, err := r.GetInterfacePeers(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load peers: %w", err)
	}

	return in, peers, nil
}

// GetPeersStats returns the stats for the given peer ids. The order of the returned stats is not guaranteed.
func (r *SqlRepo) GetPeersStats(ctx context.Context, ids ...domain.PeerIdentifier) ([]domain.PeerStatus, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	var stats []domain.PeerStatus

	err := r.db.WithContext(ctx).Where("identifier IN ?", ids).Find(&stats).Error
	if err != nil {
		return nil, err
	}

	return stats, nil
}

// GetAllInterfaces returns all interfaces.
func (r *SqlRepo) GetAllInterfaces(ctx context.Context) ([]domain.Interface, error) {
	var interfaces []domain.Interface

	err := r.db.WithContext(ctx).Preload("Addresses").Find(&interfaces).Error
	if err != nil {
		return nil, err
	}

	return interfaces, nil
}

// GetInterfaceStats returns the stats for the given interface id.
// If no stats are found, an error domain.ErrNotFound is returned.
func (r *SqlRepo) GetInterfaceStats(ctx context.Context, id domain.InterfaceIdentifier) (
	*domain.InterfaceStatus,
	error,
) {
	if id == "" {
		return nil, nil
	}

	var stats []domain.InterfaceStatus

	err := r.db.WithContext(ctx).Where("identifier = ?", id).Find(&stats).Error
	if err != nil {
		return nil, err
	}

	if len(stats) == 0 {
		return nil, domain.ErrNotFound
	}

	stat := stats[0]

	return &stat, nil
}

// FindInterfaces returns all interfaces that match the given search string.
// The search string is matched against the interface identifier and display name.
func (r *SqlRepo) FindInterfaces(ctx context.Context, search string) ([]domain.Interface, error) {
	var users []domain.Interface

	searchValue := "%" + strings.ToLower(search) + "%"
	err := r.db.WithContext(ctx).
		Where("identifier LIKE ?", searchValue).
		Or("display_name LIKE ?", searchValue).
		Preload("Addresses").
		Find(&users).Error
	if err != nil {
		return nil, err
	}

	return users, nil
}

// SaveInterface updates the interface with the given id.
func (r *SqlRepo) SaveInterface(
	ctx context.Context,
	id domain.InterfaceIdentifier,
	updateFunc func(in *domain.Interface) (*domain.Interface, error),
) error {
	userInfo := domain.GetUserInfo(ctx)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := r.getOrCreateInterface(userInfo, tx, id)
		if err != nil {
			return err // return any error will roll back
		}

		in, err = updateFunc(in)
		if err != nil {
			return err
		}

		err = r.upsertInterface(userInfo, tx, in)
		if err != nil {
			return err
		}

		// return nil will commit the whole transaction
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *SqlRepo) getOrCreateInterface(
	ui *domain.ContextUserInfo,
	tx *gorm.DB,
	id domain.InterfaceIdentifier,
) (*domain.Interface, error) {
	var in domain.Interface

	// interfaceDefaults will be applied to newly created interface records
	interfaceDefaults := domain.Interface{
		BaseModel: domain.BaseModel{
			CreatedBy: ui.UserId(),
			UpdatedBy: ui.UserId(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		Identifier: id,
	}

	err := tx.Attrs(interfaceDefaults).FirstOrCreate(&in, id).Error
	if err != nil {
		return nil, err
	}

	return &in, nil
}

func (r *SqlRepo) upsertInterface(ui *domain.ContextUserInfo, tx *gorm.DB, in *domain.Interface) error {
	in.UpdatedBy = ui.UserId()
	in.UpdatedAt = time.Now()

	err := tx.Save(in).Error
	if err != nil {
		return err
	}

	err = tx.Model(in).Association("Addresses").Replace(in.Addresses)
	if err != nil {
		return fmt.Errorf("failed to update interface addresses: %w", err)
	}

	return nil
}

// DeleteInterface deletes the interface with the given id.
func (r *SqlRepo) DeleteInterface(ctx context.Context, id domain.InterfaceIdentifier) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("interface_identifier = ?", id).Delete(&domain.Peer{}).Error
		if err != nil {
			return err
		}

		err = tx.Delete(&domain.InterfaceStatus{InterfaceId: id}).Error
		if err != nil {
			return err
		}

		err = tx.Select(clause.Associations).Delete(&domain.Interface{Identifier: id}).Error
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// GetInterfaceIps returns a map of interface identifiers to their respective IP addresses.
func (r *SqlRepo) GetInterfaceIps(ctx context.Context) (map[domain.InterfaceIdentifier][]domain.Cidr, error) {
	var ips []struct {
		domain.Cidr
		InterfaceId domain.InterfaceIdentifier `gorm:"column:interface_identifier"`
	}

	err := r.db.WithContext(ctx).
		Table("interface_addresses").
		Joins("LEFT JOIN cidrs ON interface_addresses.cidr_cidr = cidrs.cidr").
		Scan(&ips).Error
	if err != nil {
		return nil, err
	}

	result := make(map[domain.InterfaceIdentifier][]domain.Cidr)
	for _, ip := range ips {
		result[ip.InterfaceId] = append(result[ip.InterfaceId], ip.Cidr)
	}
	return result, nil
}

// endregion interfaces

// region peers

// GetPeer returns the peer with the given id.
// If no peer is found, an error domain.ErrNotFound is returned.
func (r *SqlRepo) GetPeer(ctx context.Context, id domain.PeerIdentifier) (*domain.Peer, error) {
	var peer domain.Peer

	err := r.db.WithContext(ctx).Preload("Addresses").First(&peer, id).Error

	if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &peer, nil
}

// GetInterfacePeers returns all peers associated with the given interface id.
func (r *SqlRepo) GetInterfacePeers(ctx context.Context, id domain.InterfaceIdentifier) ([]domain.Peer, error) {
	var peers []domain.Peer

	err := r.db.WithContext(ctx).Preload("Addresses").Where("interface_identifier = ?", id).Find(&peers).Error
	if err != nil {
		return nil, err
	}

	return peers, nil
}

// FindInterfacePeers returns all peers associated with the given interface id that match the given search string.
// The search string is matched against the peer identifier, display name and IP address.
func (r *SqlRepo) FindInterfacePeers(ctx context.Context, id domain.InterfaceIdentifier, search string) (
	[]domain.Peer,
	error,
) {
	var peers []domain.Peer

	searchValue := "%" + strings.ToLower(search) + "%"
	err := r.db.WithContext(ctx).Where("interface_identifier = ?", id).
		Where("identifier LIKE ?", searchValue).
		Or("display_name LIKE ?", searchValue).
		Or("iface_address_str_v LIKE ?", searchValue).
		Find(&peers).Error
	if err != nil {
		return nil, err
	}

	return peers, nil
}

// GetUserPeers returns all peers associated with the given user id.
func (r *SqlRepo) GetUserPeers(ctx context.Context, id domain.UserIdentifier) ([]domain.Peer, error) {
	var peers []domain.Peer

	err := r.db.WithContext(ctx).Preload("Addresses").Where("user_identifier = ?", id).Find(&peers).Error
	if err != nil {
		return nil, err
	}

	return peers, nil
}

// FindUserPeers returns all peers associated with the given user id that match the given search string.
// The search string is matched against the peer identifier, display name and IP address.
func (r *SqlRepo) FindUserPeers(ctx context.Context, id domain.UserIdentifier, search string) ([]domain.Peer, error) {
	var peers []domain.Peer

	searchValue := "%" + strings.ToLower(search) + "%"
	err := r.db.WithContext(ctx).Where("user_identifier = ?", id).
		Where("identifier LIKE ?", searchValue).
		Or("display_name LIKE ?", searchValue).
		Or("iface_address_str_v LIKE ?", searchValue).
		Find(&peers).Error
	if err != nil {
		return nil, err
	}

	return peers, nil
}

// SavePeer updates the peer with the given id.
// If no existing peer is found, a new peer is created.
func (r *SqlRepo) SavePeer(
	ctx context.Context,
	id domain.PeerIdentifier,
	updateFunc func(in *domain.Peer) (*domain.Peer, error),
) error {
	userInfo := domain.GetUserInfo(ctx)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		peer, err := r.getOrCreatePeer(userInfo, tx, id)
		if err != nil {
			return err // return any error will roll back
		}

		peer, err = updateFunc(peer)
		if err != nil {
			return err
		}

		err = r.upsertPeer(userInfo, tx, peer)
		if err != nil {
			return err
		}

		// return nil will commit the whole transaction
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *SqlRepo) getOrCreatePeer(ui *domain.ContextUserInfo, tx *gorm.DB, id domain.PeerIdentifier) (
	*domain.Peer,
	error,
) {
	var peer domain.Peer

	// interfaceDefaults will be applied to newly created interface records
	interfaceDefaults := domain.Peer{
		BaseModel: domain.BaseModel{
			CreatedBy: ui.UserId(),
			UpdatedBy: ui.UserId(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		Identifier: id,
	}

	err := tx.Attrs(interfaceDefaults).FirstOrCreate(&peer, id).Error
	if err != nil {
		return nil, err
	}

	return &peer, nil
}

func (r *SqlRepo) upsertPeer(ui *domain.ContextUserInfo, tx *gorm.DB, peer *domain.Peer) error {
	peer.UpdatedBy = ui.UserId()
	peer.UpdatedAt = time.Now()

	err := tx.Save(peer).Error
	if err != nil {
		return err
	}

	err = tx.Model(peer).Association("Addresses").Replace(peer.Interface.Addresses)
	if err != nil {
		return fmt.Errorf("failed to update peer addresses: %w", err)
	}

	return nil
}

// DeletePeer deletes the peer with the given id.
func (r *SqlRepo) DeletePeer(ctx context.Context, id domain.PeerIdentifier) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// NOTE: We do NOT delete peer_status here.
		// The peer_status will be cleaned up by CleanOrphanedStatuses on all cluster nodes.
		// This ensures that other nodes can detect orphaned statuses and clean up their metrics.

		err := tx.Select(clause.Associations).Delete(&domain.Peer{Identifier: id}).Error
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// GetPeerIps returns a map of peer identifiers to their respective IP addresses.
func (r *SqlRepo) GetPeerIps(ctx context.Context) (map[domain.PeerIdentifier][]domain.Cidr, error) {
	var ips []struct {
		domain.Cidr
		PeerId domain.PeerIdentifier `gorm:"column:peer_identifier"`
	}

	err := r.db.WithContext(ctx).
		Table("peer_addresses").
		Joins("LEFT JOIN cidrs ON peer_addresses.cidr_cidr = cidrs.cidr").
		Scan(&ips).Error
	if err != nil {
		return nil, err
	}

	result := make(map[domain.PeerIdentifier][]domain.Cidr)
	for _, ip := range ips {
		result[ip.PeerId] = append(result[ip.PeerId], ip.Cidr)
	}
	return result, nil
}

// GetUsedIpsPerSubnet returns a map of subnets to their respective used IP addresses.
func (r *SqlRepo) GetUsedIpsPerSubnet(ctx context.Context, subnets []domain.Cidr) (
	map[domain.Cidr][]domain.Cidr,
	error,
) {
	var peerIps []struct {
		domain.Cidr
		PeerId domain.PeerIdentifier `gorm:"column:peer_identifier"`
	}

	err := r.db.WithContext(ctx).
		Table("peer_addresses").
		Joins("LEFT JOIN cidrs ON peer_addresses.cidr_cidr = cidrs.cidr").
		Scan(&peerIps).Error
	if err != nil {
		return nil, fmt.Errorf("failed to fetch peer IP's: %w", err)
	}

	var interfaceIps []struct {
		domain.Cidr
		InterfaceId domain.InterfaceIdentifier `gorm:"column:interface_identifier"`
	}

	err = r.db.WithContext(ctx).
		Table("interface_addresses").
		Joins("LEFT JOIN cidrs ON interface_addresses.cidr_cidr = cidrs.cidr").
		Scan(&interfaceIps).Error
	if err != nil {
		return nil, fmt.Errorf("failed to fetch interface IP's: %w", err)
	}

	result := make(map[domain.Cidr][]domain.Cidr, len(subnets))
	for _, ip := range interfaceIps {
		var subnet domain.Cidr // default empty subnet (if no subnet matches, we will add the IP to the empty subnet group)
		for _, s := range subnets {
			if s.Contains(ip.Cidr) {
				subnet = s
				break
			}
		}
		result[subnet] = append(result[subnet], ip.Cidr)
	}
	for _, ip := range peerIps {
		var subnet domain.Cidr // default empty subnet (if no subnet matches, we will add the IP to the empty subnet group)
		for _, s := range subnets {
			if s.Contains(ip.Cidr) {
				subnet = s
				break
			}
		}
		result[subnet] = append(result[subnet], ip.Cidr)
	}
	return result, nil
}

// endregion peers

// region users

// GetUser returns the user with the given id.
// If no user is found, an error domain.ErrNotFound is returned.
func (r *SqlRepo) GetUser(ctx context.Context, id domain.UserIdentifier) (*domain.User, error) {
	var user domain.User

	err := r.db.WithContext(ctx).Preload("WebAuthnCredentialList").First(&user, id).Error

	if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUserByEmail returns the user with the given email.
// If no user is found, an error domain.ErrNotFound is returned.
// If multiple users are found, an error domain.ErrNotUnique is returned.
func (r *SqlRepo) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	var users []domain.User

	err := r.db.WithContext(ctx).Where("email = ?", email).Preload("WebAuthnCredentialList").Find(&users).Error
	if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, domain.ErrNotFound
	}

	if len(users) > 1 {
		return nil, fmt.Errorf("found multiple users with email %s: %w", email, domain.ErrNotUnique)
	}

	user := users[0]

	return &user, nil
}

// GetUserByWebAuthnCredential returns the user with the given webauthn credential id.
func (r *SqlRepo) GetUserByWebAuthnCredential(ctx context.Context, credentialIdBase64 string) (*domain.User, error) {
	var credential domain.UserWebauthnCredential

	err := r.db.WithContext(ctx).Where("credential_identifier = ?", credentialIdBase64).First(&credential).Error
	if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return r.GetUser(ctx, domain.UserIdentifier(credential.UserIdentifier))
}

// GetAllUsers returns all users.
func (r *SqlRepo) GetAllUsers(ctx context.Context) ([]domain.User, error) {
	var users []domain.User

	err := r.db.WithContext(ctx).Preload("WebAuthnCredentialList").Find(&users).Error
	if err != nil {
		return nil, err
	}

	return users, nil
}

// FindUsers returns all users that match the given search string.
// The search string is matched against the user identifier, firstname, lastname and email.
func (r *SqlRepo) FindUsers(ctx context.Context, search string) ([]domain.User, error) {
	var users []domain.User

	searchValue := "%" + strings.ToLower(search) + "%"
	err := r.db.WithContext(ctx).
		Where("identifier LIKE ?", searchValue).
		Or("firstname LIKE ?", searchValue).
		Or("lastname LIKE ?", searchValue).
		Or("email LIKE ?", searchValue).
		Preload("WebAuthnCredentialList").
		Find(&users).Error
	if err != nil {
		return nil, err
	}

	return users, nil
}

// SaveUser updates the user with the given id.
// If no user is found, a new user is created.
func (r *SqlRepo) SaveUser(
	ctx context.Context,
	id domain.UserIdentifier,
	updateFunc func(u *domain.User) (*domain.User, error),
) error {
	userInfo := domain.GetUserInfo(ctx)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, err := r.getOrCreateUser(userInfo, tx, id)
		if err != nil {
			return err // return any error will roll back
		}

		user, err = updateFunc(user)
		if err != nil {
			return err
		}

		err = r.upsertUser(userInfo, tx, user)
		if err != nil {
			return err
		}

		// return nil will commit the whole transaction
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// DeleteUser deletes the user with the given id.
func (r *SqlRepo) DeleteUser(ctx context.Context, id domain.UserIdentifier) error {
	err := r.db.WithContext(ctx).Unscoped().Select(clause.Associations).Delete(&domain.User{Identifier: id}).Error
	if err != nil {
		return err
	}

	return nil
}

func (r *SqlRepo) getOrCreateUser(ui *domain.ContextUserInfo, tx *gorm.DB, id domain.UserIdentifier) (
	*domain.User,
	error,
) {
	var user domain.User

	// userDefaults will be applied to newly created user records
	userDefaults := domain.User{
		BaseModel: domain.BaseModel{
			CreatedBy: ui.UserId(),
			UpdatedBy: ui.UserId(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		Identifier: id,
		Source:     domain.UserSourceDatabase,
		IsAdmin:    false,
	}

	err := tx.Attrs(userDefaults).FirstOrCreate(&user, id).Error
	if err != nil {
		return nil, err
	}

	return &user, nil
}

func (r *SqlRepo) upsertUser(ui *domain.ContextUserInfo, tx *gorm.DB, user *domain.User) error {
	user.UpdatedBy = ui.UserId()
	user.UpdatedAt = time.Now()

	err := tx.Save(user).Error
	if err != nil {
		return err
	}

	err = tx.Session(&gorm.Session{FullSaveAssociations: true}).Unscoped().Model(user).Association("WebAuthnCredentialList").Unscoped().Replace(user.WebAuthnCredentialList)
	if err != nil {
		return fmt.Errorf("failed to update users webauthn credentials: %w", err)
	}

	return nil
}

// endregion users

// region statistics

// UpdateInterfaceStatus updates the interface status with the given id.
// If no interface status is found, a new one is created.
func (r *SqlRepo) UpdateInterfaceStatus(
	ctx context.Context,
	id domain.InterfaceIdentifier,
	updateFunc func(in *domain.InterfaceStatus) (*domain.InterfaceStatus, error),
) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := r.getOrCreateInterfaceStatus(tx, id)
		if err != nil {
			return err // return any error will roll back
		}

		in, err = updateFunc(in)
		if err != nil {
			return err
		}

		err = r.upsertInterfaceStatus(tx, in)
		if err != nil {
			return err
		}

		// return nil will commit the whole transaction
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *SqlRepo) getOrCreateInterfaceStatus(tx *gorm.DB, id domain.InterfaceIdentifier) (
	*domain.InterfaceStatus,
	error,
) {
	var in domain.InterfaceStatus

	// defaults will be applied to newly created record
	defaults := domain.InterfaceStatus{
		InterfaceId: id,
		UpdatedAt:   time.Now(),
	}

	err := tx.Attrs(defaults).FirstOrCreate(&in, id).Error
	if err != nil {
		return nil, err
	}

	return &in, nil
}

func (r *SqlRepo) upsertInterfaceStatus(tx *gorm.DB, in *domain.InterfaceStatus) error {
	err := tx.Save(in).Error
	if err != nil {
		return err
	}

	return nil
}

// UpdatePeerStatus updates the peer status with the given id.
// If no peer status is found, a new one is created.
// Includes automatic retry logic with exponential backoff for deadlock/conflict recovery.
// Uses row-level FOR UPDATE locking to prevent concurrent modification conflicts.
func (r *SqlRepo) UpdatePeerStatus(
	ctx context.Context,
	id domain.PeerIdentifier,
	updateFunc func(in *domain.PeerStatus) (*domain.PeerStatus, error),
) error {
	// Retry logic with exponential backoff for deadlocks and conflicts
	// Increased to 5 retries (50ms, 100ms, 200ms, 400ms, 800ms total ~1.6s) for multi-node scenarios
	const maxRetries = 5
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 50ms, 100ms, 200ms, 400ms, 800ms
			// Longer delays give other nodes time to release locks
			waitTime := time.Duration(50*(1<<uint(attempt-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitTime):
			}
		}

		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			in, err := r.getOrCreatePeerStatus(tx, id)
			if err != nil {
				return err // return any error will roll back
			}

			in, err = updateFunc(in)
			if err != nil {
				return err
			}

			err = r.upsertPeerStatus(tx, in)
			if err != nil {
				return err
			}

			// return nil will commit the whole transaction
			return nil
		})
		if err == nil {
			return nil // success
		}

		lastErr = err
		// Check if this is a retryable error (deadlock or record changed)
		errMsg := err.Error()
		if !strings.Contains(errMsg, "Deadlock") && !strings.Contains(errMsg, "Record has changed") {
			return err // not a retryable error
		}
		// Continue to retry
	}

	return fmt.Errorf("UpdatePeerStatus failed after %d retries: %w", maxRetries, lastErr)
}

// ClaimPeerStatus claims ownership of a peer status for this node.
// Only the owner node should update peer status to avoid conflicts.
// Once claimed, only this node (and nodes with same owner_node_id) can update it.
// Uses row-level FOR UPDATE locking to serialize concurrent claims.
func (r *SqlRepo) ClaimPeerStatus(
	ctx context.Context,
	id domain.PeerIdentifier,
	ownerNodeId string,
	updateFunc func(in *domain.PeerStatus) (*domain.PeerStatus, error),
) error {
	if ownerNodeId == "" {
		return fmt.Errorf("ownerNodeId cannot be empty")
	}

	// Retry logic with exponential backoff for deadlocks and conflicts
	// Increased to 5 retries for multi-node ownership claiming
	const maxRetries = 5
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 50ms, 100ms, 200ms, 400ms, 800ms
			// Longer delays give other nodes time to release locks
			waitTime := time.Duration(50*(1<<uint(attempt-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitTime):
			}
		}

		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			in, err := r.getOrCreatePeerStatus(tx, id)
			if err != nil {
				return err
			}

			// Claim ownership: set before calling updateFunc
			in.OwnerNodeId = ownerNodeId
			in.UpdatedAt = time.Now()

			// Apply the custom update function
			in, err = updateFunc(in)
			if err != nil {
				return err
			}

			// Ensure ownership is maintained
			in.OwnerNodeId = ownerNodeId

			err = r.upsertPeerStatus(tx, in)
			if err != nil {
				return err
			}

			return nil
		})
		if err == nil {
			return nil // success
		}

		lastErr = err
		// Check if this is a retryable error (deadlock or record changed)
		errMsg := err.Error()
		if !strings.Contains(errMsg, "Deadlock") && !strings.Contains(errMsg, "Record has changed") {
			return err // not a retryable error
		}
		// Continue to retry
	}

	return fmt.Errorf("ClaimPeerStatus failed after %d retries: %w", maxRetries, lastErr)
}

// BatchUpdatePeerStatuses updates multiple peer statuses in a single optimized transaction.
// This is more efficient than calling UpdatePeerStatus individually and reduces deadlock risk.
// Uses ON CONFLICT DO UPDATE for bulk upsert with automatic conflict resolution.
func (r *SqlRepo) BatchUpdatePeerStatuses(
	ctx context.Context,
	updates map[domain.PeerIdentifier]func(in *domain.PeerStatus) (*domain.PeerStatus, error),
) error {
	if len(updates) == 0 {
		return nil
	}

	// Retry logic for batch operations - more retries for bulk operations
	const maxRetries = 5
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 50ms, 100ms, 200ms, 400ms, 800ms
			waitTime := time.Duration(50*(1<<uint(attempt-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitTime):
			}
		}

		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var allStatuses []*domain.PeerStatus

			// Load all peer statuses we need to update
			for id := range updates {
				in, err := r.getOrCreatePeerStatus(tx, id)
				if err != nil {
					return err
				}

				// Apply the update function
				updateFunc := updates[id]
				in, err = updateFunc(in)
				if err != nil {
					return err
				}

				allStatuses = append(allStatuses, in)
			}

			// Batch insert/update all statuses in one operation
			if len(allStatuses) > 0 {
				err := tx.Clauses(clause.OnConflict{
					UpdateAll: true,
				}).CreateInBatches(allStatuses, 50).Error // batch insert in groups of 50
				if err != nil {
					return err
				}
			}

			return nil
		})
		if err == nil {
			return nil // success
		}

		lastErr = err
		// Check if this is a retryable error
		errMsg := err.Error()
		if !strings.Contains(errMsg, "Deadlock") && !strings.Contains(errMsg, "Record has changed") {
			return err // not a retryable error
		}
		// Continue to retry
	}

	return fmt.Errorf("BatchUpdatePeerStatuses failed after %d retries: %w", maxRetries, lastErr)
}

func (r *SqlRepo) getOrCreatePeerStatus(tx *gorm.DB, id domain.PeerIdentifier) (*domain.PeerStatus, error) {
	var in domain.PeerStatus

	// defaults will be applied to newly created record
	defaults := domain.PeerStatus{
		PeerId:    id,
		UpdatedAt: time.Now(),
	}

	// Use Clauses(clause.Locking{Strength: "UPDATE"}) to add FOR UPDATE
	// This locks the row at the database level, preventing concurrent modifications
	// If row doesn't exist, FirstOrCreate will create it
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Attrs(defaults).FirstOrCreate(&in, id).Error
	if err != nil {
		return nil, err
	}

	return &in, nil
}

func (r *SqlRepo) getOrCreatePeerStatusForRead(tx *gorm.DB, id domain.PeerIdentifier) (*domain.PeerStatus, error) {
	var in domain.PeerStatus

	// defaults will be applied to newly created record
	defaults := domain.PeerStatus{
		PeerId:    id,
		UpdatedAt: time.Now(),
	}

	// For read-only access without modifications (used in conditionals)
	// No FOR UPDATE lock - allows concurrent reads
	err := tx.Attrs(defaults).FirstOrCreate(&in, id).Error
	if err != nil {
		return nil, err
	}

	return &in, nil
}

func (r *SqlRepo) upsertPeerStatus(tx *gorm.DB, in *domain.PeerStatus) error {
	err := tx.Save(in).Error
	if err != nil {
		return err
	}

	return nil
}

// DeletePeerStatus deletes the peer status with the given id.
func (r *SqlRepo) DeletePeerStatus(ctx context.Context, id domain.PeerIdentifier) error {
	err := r.db.WithContext(ctx).Delete(&domain.PeerStatus{}, id).Error
	if err != nil {
		return err
	}

	return nil
}

// GetAllPeerStatuses returns all peer statuses from the database.
func (r *SqlRepo) GetAllPeerStatuses(ctx context.Context) ([]domain.PeerStatus, error) {
	var statuses []domain.PeerStatus

	err := r.db.WithContext(ctx).Find(&statuses).Error
	if err != nil {
		return nil, err
	}

	return statuses, nil
}

// endregion statistics

// region audit

// SaveAuditEntry saves the given audit entry.
func (r *SqlRepo) SaveAuditEntry(ctx context.Context, entry *domain.AuditEntry) error {
	err := r.db.WithContext(ctx).Save(entry).Error
	if err != nil {
		return err
	}

	return nil
}

// GetAllAuditEntries retrieves all audit entries from the database.
// The entries are ordered by timestamp, with the newest entries first.
func (r *SqlRepo) GetAllAuditEntries(ctx context.Context) ([]domain.AuditEntry, error) {
	var entries []domain.AuditEntry
	err := r.db.WithContext(ctx).Order("created_at desc").Find(&entries).Error
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// endregion audit

func (r *SqlRepo) GetAllPeers(ctx context.Context) ([]domain.Peer, error) {
	var peers []domain.Peer
	// OPTIMIZED: Select only peer identifiers and basic fields to avoid N+1 queries
	// from Preload("Addresses") and Preload("Interface") which would create 600+ queries
	// This method is used mainly for cleanup validation, not for full peer data display
	err := r.db.WithContext(ctx).
		Select("id, identifier, display_name, interface_identifier").
		Find(&peers).Error
	if err != nil {
		return nil, err
	}

	return peers, nil
}

// SyncAllPeersFromDB synchronizes all peers from the database.
func (r *SqlRepo) SyncAllPeersFromDB(ctx context.Context) (int, error) {
	slog.Debug("SyncAllPeersFromDB called, but no operation performed")
	return 0, nil
}