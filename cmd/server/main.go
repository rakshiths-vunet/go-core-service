// Package main provides the entry point for the go-core-service application.
//
// This file initializes and starts:
// 1. A gRPC server (port 9090) registering the CoreServer service for item lookups.
// 2. An HTTP server (port 8090) exposing a health check endpoint (/healthz) and a Gin-based API (/api/orders).
//
// The /api/orders endpoint acts as a middle-tier coordinator:
// - Receives incoming JSON order requests.
// - Logs request details using both log/slog and logrus.
// - Performs a loopback gRPC call to ItemLookup.
// - Forwards the request downstream to the Java order service (java-order-service).
// - Aggregates and returns the combined response to the caller.
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go-core-service/internal/corepb"
	"go-core-service/internal/journey"
	"go-core-service/internal/server"
)

const (
	httpAddr = ":8090"
	grpcAddr = ":9090"
)

func listenAddrs() (httpAddr, grpcAddr string) {
	httpAddr = os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8090"
	}
	grpcAddr = os.Getenv("GRPC_ADDR")
	if grpcAddr == "" {
		grpcAddr = ":9090"
	}
	return httpAddr, grpcAddr
}

type orderRequest struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

func javaServiceURL() string {
	if url := os.Getenv("JAVA_SERVICE_URL"); url != "" {
		return url
	}
	return "http://localhost:8080"
}

func corsMiddleware(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}

// journeyAuthMiddleware optionally gates the journey control endpoints behind a
// shared token. It is disabled entirely unless JOURNEY_API_TOKEN is set.
func journeyAuthMiddleware() gin.HandlerFunc {
	token := os.Getenv("JOURNEY_API_TOKEN")
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		if c.GetHeader("Authorization") != "Bearer "+token &&
			c.GetHeader("X-Journey-Token") != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	httpAddr, grpcAddr := listenAddrs()

	grpcServer := grpc.NewServer()
	corepb.RegisterCoreServer(grpcServer, &server.CoreServer{})

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", grpcAddr, err)
	}
	go func() {
		slog.Info("grpc server listening", "addr", grpcAddr)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("grpc server error: %v", err)
		}
	}()

	grpcConn, err := grpc.NewClient("localhost"+grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to create grpc client: %v", err)
	}
	grpcClient := corepb.NewCoreClient(grpcConn)

	httpClient := &http.Client{}

	gin.SetMode(gin.ReleaseMode)
	ginEngine := gin.New()
	ginEngine.Use(gin.Recovery(), corsMiddleware)

	journeyManager := journey.New()
	journeyRoutes := ginEngine.Group("/api/journey")
	journeyRoutes.Use(journeyAuthMiddleware())
	journeyRoutes.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, journeyManager.Status())
	})
	journeyRoutes.POST("/start", func(c *gin.Context) {
		var params journey.Params
		_ = c.ShouldBindJSON(&params)
		st, err := journeyManager.Start(params)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	journeyRoutes.POST("/stop", func(c *gin.Context) {
		st, err := journeyManager.Stop()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	// CI hook: matches the deploy.yml "Trigger synmon" stub
	// ($SYNMON_URL/trigger with body {"target": "..."}).
	journeyRoutes.POST("/trigger", func(c *gin.Context) {
		var req struct {
			Target string `json:"target"`
		}
		_ = c.ShouldBindJSON(&req)
		st, err := journeyManager.Start(journey.Params{TargetURL: req.Target})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})

	ginEngine.POST("/api/orders", func(c *gin.Context) {
		var req orderRequest
		_ = c.ShouldBindJSON(&req) // fields optional/ignored

		slog.Info("received order request", "item", req.Item, "qty", req.Qty)
		logrus.WithFields(logrus.Fields{"item": req.Item, "qty": req.Qty}).Info("received order request")

		grpcStatus := "ok"
		if _, err := grpcClient.ItemLookup(c.Request.Context(), &corepb.ItemRequest{Item: req.Item}); err != nil {
			slog.Error("grpc ItemLookup failed", "error", err)
			grpcStatus = "error"
		} else {
			slog.Info("grpc ItemLookup succeeded")
		}

		body, _ := json.Marshal(req)
		javaResp, err := httpClient.Post(javaServiceURL()+"/api/process", "application/json", bytes.NewReader(body))
		javaStatus := "ok"
		var downstream any
		if err != nil {
			slog.Error("call to java service failed", "error", err)
			javaStatus = "error"
			downstream = map[string]string{"error": err.Error()}
		} else {
			defer javaResp.Body.Close()
			_ = json.NewDecoder(javaResp.Body).Decode(&downstream)
		}

		c.JSON(http.StatusOK, gin.H{
			"orderId":     uuid.NewString(),
			"item":        req.Item,
			"qty":         req.Qty,
			"processedBy": "go-core-service",
			"triggered": gin.H{
				"http":   "ok",
				"slog":   "ok",
				"logrus": "ok",
				"grpc":   grpcStatus,
				"java":   javaStatus,
			},
			"downstream": downstream,
		})
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("/api/", ginEngine)

	slog.Info("http server listening", "addr", httpAddr)
	if err := http.ListenAndServe(httpAddr, mux); err != nil {
		log.Fatalf("http server error: %v", err)
	}
}
