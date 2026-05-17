// Package xds implements the Envoy xDS gRPC server (ADS) for service discovery.
// Omega-LB acts as the xDS management server — Envoy/Istio/custom clients can
// connect and receive cluster/endpoint updates via the same API Envoy uses.
package xds

import (
	"context"
	"net"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/ring"
)

// Server is the gRPC xDS management server.
type Server struct {
	cfg    config.XDSConfig
	log    *zap.Logger
	ring   *ring.Manager
	grpcSrv *grpc.Server
}

// NewServer constructs the xDS server.
func NewServer(cfg config.XDSConfig, rm *ring.Manager, log *zap.Logger) (*Server, error) {
	grpcSrv := grpc.NewServer(
		grpc.MaxRecvMsgSize(4<<20),
		grpc.MaxSendMsgSize(4<<20),
	)
	reflection.Register(grpcSrv)

	s := &Server{cfg: cfg, log: log, ring: rm, grpcSrv: grpcSrv}
	// Real impl: register envoy ADS service handlers here using
	// go-control-plane's cache.NewSnapshotCache and server.NewServer.
	return s, nil
}

// Serve starts the gRPC xDS server and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	s.log.Info("xDS gRPC server listening", zap.String("addr", s.cfg.ListenAddr))

	go func() {
		<-ctx.Done()
		s.grpcSrv.GracefulStop()
	}()

	return s.grpcSrv.Serve(lis)
}

// NotifyUpdate pushes a new snapshot to connected xDS clients when the ring changes.
// Real impl: update the go-control-plane SnapshotCache with the new EDS snapshot.
func (s *Server) NotifyUpdate(version string) {
	s.log.Info("xDS snapshot update", zap.String("version", version))
}
