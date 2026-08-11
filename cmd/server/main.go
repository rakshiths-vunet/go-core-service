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
	"go-core-service/internal/server"
)

const (
	httpAddr = ":8090"
	grpcAddr = ":9090"
)

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

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

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
