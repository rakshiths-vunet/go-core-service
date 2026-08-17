// Package server implements the gRPC service handlers defined in the protobuf schemas.
//
// This file provides the concrete implementation of the corepb.CoreServer interface.
// Its CoreServer struct handles incoming gRPC requests such as ItemLookup, executing service-level
// operations and returning protobuf-defined responses.
package server

import (
	"context"
	"log/slog"

	"go-core-service/internal/corepb"
)

// CoreServer implements the corepb.CoreServer gRPC interface.
type CoreServer struct {
	corepb.UnimplementedCoreServer
}

func (s *CoreServer) ItemLookup(ctx context.Context, req *corepb.ItemRequest) (*corepb.ItemResponse, error) {
	slog.Info("grpc ItemLookup called", "item", req.GetItem())
	return &corepb.ItemResponse{Status: "found"}, nil
}
