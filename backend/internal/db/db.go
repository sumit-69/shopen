package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/lib/pq"
	"go.uber.org/zap"

	"go.opentelemetry.io/otel/attribute"

	"github.com/shopen/backend/internal/logger"
	"github.com/shopen/backend/internal/models"
)

const (
	queryTimeout = 3 * time.Second
	maxShopLimit = 100
)

// DB wraps *sql.DB and provides all database operations.
type DB struct {
	conn *sql.DB
}

// New opens a PostgreSQL connection and verifies it with a ping.
func New() (*DB, error) {

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		getenv("DB_HOST", "localhost"),
		getenv("DB_PORT", "5432"),
		getenv("DB_USER", "postgres"),
		getenv("DB_PASSWORD", ""),
		getenv("DB_NAME", "shopen"),
		getenv("DB_SSLMODE", "disable"),
	)

	driverName, err := otelsql.Register(
		"postgres",
		otelsql.WithAttributes(
			attribute.String("db.system", "postgresql"),
		),
	)
	if err != nil && !strings.Contains(err.Error(), "already registered") {
		return nil, fmt.Errorf("register otel sql: %w", err)
	}

	conn, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	conn.SetMaxOpenConns(50)
	conn.SetMaxIdleConns(25)
	conn.SetConnMaxLifetime(30 * time.Minute)
	conn.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	logger.Log.Info("database connected")

	return &DB{conn: conn}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// Ping checks database connectivity.
func (d *DB) Ping(ctx context.Context) error {
	return d.conn.PingContext(ctx)
}

// ─── SHOP QUERIES ─────────────────────────────────────────────────────────────

// ListShops returns all shops matching the given filter.
func (d *DB) ListShops(ctx context.Context, f models.ShopFilter) ([]models.Shop, error) {
	query := `
		SELECT id, name, category, subcat, icon, address, phone, hours,
		       is_open, description, photo_url, map_query, created_at, updated_at
		FROM shops
		WHERE 1=1`

	args := make([]interface{}, 0)
	argIdx := 1

	if f.Category != "" {
		query += fmt.Sprintf(" AND category = $%d", argIdx)
		args = append(args, f.Category)
		argIdx++
	}

	if f.Subcat != "" {
		query += fmt.Sprintf(" AND subcat = $%d", argIdx)
		args = append(args, f.Subcat)
		argIdx++
	}

	switch f.Status {
	case "":
		// no filter

	case "open":
		query += fmt.Sprintf(" AND is_open = $%d", argIdx)
		args = append(args, true)
		argIdx++

	case "closed":
		query += fmt.Sprintf(" AND is_open = $%d", argIdx)
		args = append(args, false)
		argIdx++

	default:
		return nil, fmt.Errorf("invalid status filter")
	}

	if f.Search != "" {
		query += fmt.Sprintf(
			" AND (name ILIKE $%d OR address ILIKE $%d OR subcat ILIKE $%d)",
			argIdx, argIdx+1, argIdx+2,
		)
		like := "%" + f.Search + "%"
		args = append(args, like, like, like)
		argIdx += 3
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", maxShopLimit)

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		logger.Log.Error("db query failed",
			zap.Error(err),
		)
		return nil, fmt.Errorf("list shops: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list shops: %w", err)
	}
	defer rows.Close()

	shops := make([]models.Shop, 0, 20)
	for rows.Next() {
		var s models.Shop
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Category, &s.Subcat, &s.Icon,
			&s.Address, &s.Phone, &s.Hours, &s.IsOpen,
			&s.Description, &s.PhotoURL, &s.MapQuery,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			logger.Log.Error("scan shop failed", zap.Error(err))
			return nil, fmt.Errorf("scan shop: %w", err)
		}
		shops = append(shops, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return shops, nil

}

// GetShopByID returns a single shop by its ID.
func (d *DB) GetShopByID(id int) (*models.Shop, error) {
	const query = `
		SELECT id, name, category, subcat, icon, address, phone, hours,
		       is_open, description, photo_url, map_query, created_at, updated_at
		FROM shops WHERE id = $1`

	var s models.Shop

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &s.Name, &s.Category, &s.Subcat, &s.Icon,
		&s.Address, &s.Phone, &s.Hours, &s.IsOpen,
		&s.Description, &s.PhotoURL, &s.MapQuery,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get shop: %w", err)
	}
	return &s, nil
}

// CreateShop inserts a new shop and returns it with its generated ID.
func (d *DB) CreateShop(req models.CreateShopRequest) (*models.Shop, error) {
	const query = `
		INSERT INTO shops (name, category, subcat, icon, address, phone, hours,
		                   is_open, description, photo_url, map_query)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, name, category, subcat, icon, address, phone, hours,
		          is_open, description, photo_url, map_query, created_at, updated_at`

	icon := req.Icon
	if icon == "" {
		icon = "🏪"
	}

	var s models.Shop
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query,
		req.Name,
		req.Category,
		req.Subcat,
		icon,
		req.Address,
		req.Phone,
		req.Hours,
		req.IsOpen,
		req.Description,
		req.PhotoURL,
		req.MapQuery,
	).Scan(
		&s.ID, &s.Name, &s.Category, &s.Subcat, &s.Icon,
		&s.Address, &s.Phone, &s.Hours, &s.IsOpen,
		&s.Description, &s.PhotoURL, &s.MapQuery,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create shop: %w", err)
	}
	return &s, nil
}

// UpdateShop applies partial updates to a shop.
func (d *DB) UpdateShop(id int, req models.UpdateShopRequest) (*models.Shop, error) {
	setClauses := []string{}
	args := make([]interface{}, 0)
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name=$%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.Category != nil {
		setClauses = append(setClauses, fmt.Sprintf("category=$%d", argIdx))
		args = append(args, *req.Category)
		argIdx++
	}
	if req.Subcat != nil {
		setClauses = append(setClauses, fmt.Sprintf("subcat=$%d", argIdx))
		args = append(args, *req.Subcat)
		argIdx++
	}
	if req.Icon != nil {
		setClauses = append(setClauses, fmt.Sprintf("icon=$%d", argIdx))
		args = append(args, *req.Icon)
		argIdx++
	}
	if req.Address != nil {
		setClauses = append(setClauses, fmt.Sprintf("address=$%d", argIdx))
		args = append(args, *req.Address)
		argIdx++
	}
	if req.Phone != nil {
		setClauses = append(setClauses, fmt.Sprintf("phone=$%d", argIdx))
		args = append(args, *req.Phone)
		argIdx++
	}
	if req.Hours != nil {
		setClauses = append(setClauses, fmt.Sprintf("hours=$%d", argIdx))
		args = append(args, *req.Hours)
		argIdx++
	}
	if req.IsOpen != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_open=$%d", argIdx))
		args = append(args, *req.IsOpen)
		argIdx++
	}
	if req.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description=$%d", argIdx))
		args = append(args, *req.Description)
		argIdx++
	}
	if req.PhotoURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("photo_url=$%d", argIdx))
		args = append(args, *req.PhotoURL)
		argIdx++
	}
	if req.MapQuery != nil {
		setClauses = append(setClauses, fmt.Sprintf("map_query=$%d", argIdx))
		args = append(args, *req.MapQuery)
		argIdx++
	}

	if len(setClauses) == 0 {
		return d.GetShopByID(id)
	}

	query := fmt.Sprintf(`
		UPDATE shops SET %s, updated_at=NOW()
		WHERE id=$%d
		RETURNING id, name, category, subcat, icon, address, phone, hours,
		          is_open, description, photo_url, map_query, created_at, updated_at`,
		strings.Join(setClauses, ", "), argIdx,
	)
	args = append(args, id)

	var s models.Shop
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query, args...).Scan(
		&s.ID, &s.Name, &s.Category, &s.Subcat, &s.Icon,
		&s.Address, &s.Phone, &s.Hours, &s.IsOpen,
		&s.Description, &s.PhotoURL, &s.MapQuery,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update shop: %w", err)
	}
	return &s, nil
}

// DeleteShop removes a shop by ID. Returns false if not found.
func (d *DB) DeleteShop(id int) (bool, error) {

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	res, err := d.conn.ExecContext(ctx, "DELETE FROM shops WHERE id=$1", id)
	if err != nil {
		return false, fmt.Errorf("delete shop: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return n > 0, nil
}

// ToggleShopStatus flips the is_open boolean for a given shop.
func (d *DB) ToggleShopStatus(id int) (*models.Shop, error) {
	const query = `
		UPDATE shops SET is_open = NOT is_open, updated_at = NOW()
		WHERE id = $1
		RETURNING id, name, category, subcat, icon, address, phone, hours,
		          is_open, description, photo_url, map_query, created_at, updated_at`

	var s models.Shop

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &s.Name, &s.Category, &s.Subcat, &s.Icon,
		&s.Address, &s.Phone, &s.Hours, &s.IsOpen,
		&s.Description, &s.PhotoURL, &s.MapQuery,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("toggle status: %w", err)
	}
	return &s, nil
}

// GetStats returns aggregate shop statistics.
func (d *DB) GetStats() (models.StatsResponse, error) {
	const query = `
		SELECT
			COUNT(*)                                  AS total,
			COUNT(*) FILTER (WHERE is_open = TRUE)    AS open,
			COUNT(*) FILTER (WHERE is_open = FALSE)   AS closed
		FROM shops`

	var s models.StatsResponse

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query).Scan(&s.Total, &s.Open, &s.Closed)
	if err != nil {
		return s, fmt.Errorf("get stats: %w", err)
	}
	if s.Total > 0 {
		s.OpenRate = int(float64(s.Open) / float64(s.Total) * 100)
	}
	return s, nil
}

// ─── ADMIN QUERIES ────────────────────────────────────────────────────────────

// GetAdminByUsername looks up an admin by username.
func (d *DB) GetAdminByUsername(username string) (*models.Admin, error) {
	const query = `SELECT id, username, password, created_at, updated_at FROM admins WHERE username=$1`
	var a models.Admin

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	err := d.conn.QueryRowContext(ctx, query, username).Scan(
		&a.ID, &a.Username, &a.Password, &a.CreatedAt, &a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get admin: %w", err)
	}
	return &a, nil
}

// ─── HELPERS ──────────────────────────────────────────────────────────────────

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
