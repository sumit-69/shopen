package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/shopen/backend/internal/cache"
	"github.com/shopen/backend/internal/db"
	"github.com/shopen/backend/internal/logger"
	"github.com/shopen/backend/internal/models"
)

const shopCacheTTL = 5 * time.Minute

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	DB *db.DB
}

// New creates a new Handler with the given database.
func New(database *db.DB) *Handler {
	return &Handler{DB: database}
}

// ─── HELPERS ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Log.Error("response encode failed", zap.Error(err))
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, models.APIResponse{Success: false, Error: msg})
}

func writeSuccess(w http.ResponseWriter, status int, data interface{}, msg string) {
	writeJSON(w, status, models.APIResponse{Success: true, Data: data, Message: msg})
}

// ─── AUTH HANDLERS ────────────────────────────────────────────────────────────

// Login handles POST /api/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req models.LoginRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	logger.Log.Info("login attempt",
		zap.String("username", req.Username),
	)

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	admin, err := h.DB.GetAdminByUsername(req.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	logger.Log.Debug("admin lookup",
		zap.Bool("found", admin != nil),
	)

	if admin == nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// verify password
	// if err := bcrypt.CompareHashAndPassword(
	// 	[]byte(admin.Password),
	// 	[]byte(req.Password),
	// ); err != nil {

	// 	logger.Log.Debug("password mismatch",
	// 		zap.String("username", req.Username),
	// 	)

	// 	writeError(w, http.StatusUnauthorized, "invalid credentials")
	// 	return
	// }
	
	// Generate JWT
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		logger.Log.Fatal("JWT_SECRET not set")
	}

	expiryHours := 24
	if h := os.Getenv("JWT_EXPIRY_HOURS"); h != "" {
		if v, err := strconv.Atoi(h); err == nil {
			expiryHours = v
		}
	}

	claims := jwt.MapClaims{
		"sub": admin.Username,
		"exp": time.Now().Add(time.Duration(expiryHours) * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate token")
		return
	}

	writeSuccess(w, http.StatusOK, models.LoginResponse{
		Token:    signed,
		Username: admin.Username,
		Message:  "login successful",
	}, "login successful")
}

// ─── PUBLIC SHOP HANDLERS ─────────────────────────────────────────────────────

// ListShops handles GET /api/shops
// Query params: category, subcat, status (open|closed), search
func (h *Handler) ListShops(w http.ResponseWriter, r *http.Request) {

	q := r.URL.Query()

	filter := models.ShopFilter{
		Category: q.Get("category"),
		Subcat:   q.Get("subcat"),
		Status:   q.Get("status"),
		Search:   q.Get("search"),
	}

	// cache key (include filters to avoid wrong cache)
	cacheKey := fmt.Sprintf(
		"shops:list:%s:%s:%s:%s",
		filter.Category,
		filter.Subcat,
		filter.Status,
		url.QueryEscape(filter.Search),
	)

	// 1️⃣ Try Redis cache
	val, err := cache.Get(cacheKey)

	if err == nil {

		var shops []models.Shop

		if err := json.Unmarshal([]byte(val), &shops); err == nil {
			logger.Log.Info("shops cache hit")
			writeSuccess(w, http.StatusOK, shops, "")
			return
		} else {
			logger.Log.Warn("redis cache decode failed", zap.Error(err))
		}
	}

	// 2️⃣ Cache miss → query DB
	shops, err := h.DB.ListShops(r.Context(), filter)
	if err != nil {
		logger.Log.Error("failed to fetch shops",
			zap.Error(err),
		)

		writeError(w, http.StatusInternalServerError, "failed to fetch shops")
		return
	}

	// 3️⃣ Store in Redis

	jsonData, err := json.Marshal(shops)
	if err != nil {
		logger.Log.Warn("failed to marshal shops for cache", zap.Error(err))
	} else {
		cache.Set(cacheKey, jsonData, shopCacheTTL)
	}

	logger.Log.Info("shops cache miss",
		zap.String("category", filter.Category),
		zap.String("subcat", filter.Subcat),
		zap.String("status", filter.Status),
	)

	writeSuccess(w, http.StatusOK, shops, "")
}

// GetShop handles GET /api/shops/{id}
func (h *Handler) GetShop(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid shop id")
		return
	}

	shop, err := h.DB.GetShopByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch shop")
		return
	}
	if shop == nil {
		writeError(w, http.StatusNotFound, "shop not found")
		return
	}
	writeSuccess(w, http.StatusOK, shop, "")
}

// ─── ADMIN SHOP HANDLERS ──────────────────────────────────────────────────────

// CreateShop handles POST /api/admin/shops
func (h *Handler) CreateShop(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req models.CreateShopRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.Category == "" || req.Subcat == "" {
		writeError(w, http.StatusBadRequest, "name, category, and subcat are required")
		return
	}

	shop, err := h.DB.CreateShop(req)

	if err != nil {
		logger.Log.Error("failed to create shop",
			zap.Error(err),
		)

		writeError(w, http.StatusInternalServerError, "failed to create shop")
		return
	}

	// 🧠 Cache invalidation
	cache.ClearShopCache()

	logger.Log.Info("shop created",
		zap.String("name", shop.Name),
		zap.String("category", shop.Category),
		zap.Int("id", shop.ID),
	)

	writeSuccess(w, http.StatusCreated, shop, "shop created successfully")
}

// UpdateShop handles PUT /api/admin/shops/{id}
func (h *Handler) UpdateShop(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid shop id")
		return
	}

	var req models.UpdateShopRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	shop, err := h.DB.UpdateShop(id, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update shop")
		return
	}

	cache.ClearShopCache()

	if shop == nil {
		writeError(w, http.StatusNotFound, "shop not found")
		return
	}
	writeSuccess(w, http.StatusOK, shop, "shop updated successfully")
}

// DeleteShop handles DELETE /api/admin/shops/{id}
func (h *Handler) DeleteShop(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid shop id")
		return
	}

	found, err := h.DB.DeleteShop(id)
	cache.ClearShopCache()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete shop")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "shop not found")
		return
	}
	writeSuccess(w, http.StatusOK, nil, "shop deleted successfully")
}

// ToggleShopStatus handles PATCH /api/admin/shops/{id}/toggle
func (h *Handler) ToggleShopStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid shop id")
		return
	}

	shop, err := h.DB.ToggleShopStatus(id)
	cache.ClearShopCache()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to toggle status")
		return
	}
	if shop == nil {
		writeError(w, http.StatusNotFound, "shop not found")
		return
	}

	status := "closed"
	if shop.IsOpen {
		status = "open"
	}
	writeSuccess(w, http.StatusOK, shop, "shop is now "+status)
}

// GetStats handles GET /api/admin/stats
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.DB.GetStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch stats")
		return
	}
	writeSuccess(w, http.StatusOK, stats, "")
}

// HealthCheck handles GET /api/health

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// check postgres
	if err := h.DB.Ping(ctx); err != nil {

		logger.Log.Error("database health check failed",
			zap.Error(err),
		)

		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	// check redis
	if err := cache.Client.Ping(cache.Ctx).Err(); err != nil {

		logger.Log.Error("redis health check failed",
			zap.Error(err),
		)

		writeError(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"service": "shopen-api",
		"version": "1.0.0",
		"time":    time.Now().UTC(),
	})
}
