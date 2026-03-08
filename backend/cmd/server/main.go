package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/shopen/backend/internal/cache"
	"github.com/shopen/backend/internal/db"
	"github.com/shopen/backend/internal/handlers"
	"github.com/shopen/backend/internal/logger"
	"github.com/shopen/backend/internal/metrics"
	"github.com/shopen/backend/internal/middleware"
	"github.com/shopen/backend/internal/tracing"
)

func main() {

	// ─────────────────────────────────────────────
	// Load environment variables
	// ─────────────────────────────────────────────
	_ = godotenv.Load()

	env := os.Getenv("ENV")
	if env == "" {
		env = "development"
	}

	// ─────────────────────────────────────────────
	// Initialize tracing
	// ─────────────────────────────────────────────
	shutdownTracer := tracing.InitTracer()
	defer shutdownTracer()

	// ─────────────────────────────────────────────
	// Initialize logger
	// ─────────────────────────────────────────────
	logger.Init()
	defer logger.Log.Sync()

	logger.Log.Info("starting shopen api",
		zap.String("env", env),
	)

	// ─────────────────────────────────────────────
	// Initialize metrics
	// ─────────────────────────────────────────────
	metrics.Init()

	// ─────────────────────────────────────────────
	// Database connection
	// ─────────────────────────────────────────────
	database, err := db.New()
	if err != nil {
		logger.Log.Fatal("database connection failed",
			zap.Error(err),
		)
	}
	defer database.Close()

	logger.Log.Info("database connected")

	// ─────────────────────────────────────────────
	// Redis connection
	// ─────────────────────────────────────────────
	cache.Init()

	logger.Log.Info("redis connected")

	// ─────────────────────────────────────────────
	// Handlers
	// ─────────────────────────────────────────────
	h := handlers.New(database)

	// ─────────────────────────────────────────────
	// Router
	// ─────────────────────────────────────────────
	r := chi.NewRouter()

	// Core middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.CleanPath)

	// Protection middleware
	r.Use(middleware.TimeoutMiddleware(5 * time.Second))
	r.Use(middleware.RateLimit)

	// Observability
	r.Use(logger.LoggingMiddleware)
	r.Use(middleware.MetricsMiddleware)

	// Security headers
	r.Use(chimiddleware.SetHeader("X-Content-Type-Options", "nosniff"))
	r.Use(chimiddleware.SetHeader("X-Frame-Options", "DENY"))
	r.Use(chimiddleware.SetHeader("X-XSS-Protection", "1; mode=block"))
	r.Use(chimiddleware.SetHeader("Server", "shopen-api"))

	// ─────────────────────────────────────────────
	// CORS
	// ─────────────────────────────────────────────
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:5173",
			"http://localhost:3000",
			"https://shopen.app",
		},
		AllowedMethods: []string{
			"GET",
			"POST",
			"PUT",
			"PATCH",
			"DELETE",
			"OPTIONS",
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"X-Request-ID",
		},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ─────────────────────────────────────────────
	// Routes
	// ─────────────────────────────────────────────

	// Prometheus metrics
	r.Method(http.MethodGet, "/metrics", middleware.MetricsHandler())

	// Health
	r.Get("/api/health", h.HealthCheck)

	// Public routes
	r.Group(func(r chi.Router) {

		r.Get("/api/shops", h.ListShops)
		r.Get("/api/shops/{id}", h.GetShop)

	})

	// Auth
	r.Post("/api/auth/login", h.Login)

	// Admin routes
	r.Group(func(r chi.Router) {

		r.Use(middleware.JWTAuth)

		r.Get("/api/admin/stats", h.GetStats)
		r.Get("/api/admin/shops", h.ListShops)

		r.Post("/api/admin/shops", h.CreateShop)
		r.Put("/api/admin/shops/{id}", h.UpdateShop)

		r.Delete("/api/admin/shops/{id}", h.DeleteShop)
		r.Patch("/api/admin/shops/{id}/toggle", h.ToggleShopStatus)

	})

	// ─────────────────────────────────────────────
	// HTTP Server
	// ─────────────────────────────────────────────

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := fmt.Sprintf("0.0.0.0:%s", port)

	logger.Log.Info("server ready",
		zap.String("address", addr),
		zap.String("env", env),
	)

	handler := otelhttp.NewHandler(r, "http-server")

	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// ─────────────────────────────────────────────
	// Start server
	// ─────────────────────────────────────────────
	go func() {

		logger.Log.Info("http server started")

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {

			logger.Log.Fatal("server failed",
				zap.Error(err),
			)

		}

	}()

	// ─────────────────────────────────────────────
	// Graceful shutdown
	// ─────────────────────────────────────────────
	stop := make(chan os.Signal, 1)

	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	<-stop

	logger.Log.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {

		logger.Log.Error("server shutdown failed",
			zap.Error(err),
		)

	} else {

		logger.Log.Info("server shutdown completed")

	}

}
