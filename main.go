package main

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"systemiq.ai/auth"
	"systemiq.ai/protos"
)

var testMode bool

// dialObserver dials once and returns a READY-to-use client/stub.
func dialObserver(endpoint string) (*grpc.ClientConn, protos.DataObserverClient, error) {
	var opts []grpc.DialOption
	if strings.HasSuffix(endpoint, ":443") {
		log.Println("Using TLS for Observer connection")
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	} else {
		log.Println("Using insecure connection for Observer (non-443 endpoint)")
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// Keep-alive pings even when idle to detect half-opens.
	ka := keepalive.ClientParameters{
		Time:                2 * time.Minute,
		Timeout:             20 * time.Second,
		PermitWithoutStream: true,
	}
	opts = append(opts, grpc.WithKeepaliveParams(ka))

	// Dial back-off parameters.
	opts = append(opts, grpc.WithConnectParams(grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  1 * time.Second,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   30 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}))

	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, nil, err
	}
	return conn, protos.NewDataObserverClient(conn), nil
}

/* -------------------- gRPC server -------------------- */

type ObserverMiddlewareServer struct {
	protos.UnimplementedDataObserverServer
	client      protos.DataObserverClient
	authHandler *auth.AuthHandler
}

func (s *ObserverMiddlewareServer) ObserveData(
	ctx context.Context,
	req *protos.ObservationRequest,
) (*protos.ObservationResponse, error) {

	// Test-mode short-circuit
	if testMode {
		return &protos.ObservationResponse{Status: "success"}, nil
	}

	// Fresh JWT each call
	token, err := s.authHandler.GetToken()
	if err != nil {
		return nil, err
	}
	req.Token = &token

	// 5-second deadline & WaitForReady so first call after restart waits
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return s.client.ObserveData(ctx, req, grpc.WaitForReady(true))
}

func main() {
	/* ---------- configuration ---------- */
	if v := os.Getenv("TEST_MODE"); strings.ToLower(v) == "true" || v == "1" {
		testMode = true
		log.Println("Running in TEST MODE – external Observer calls are skipped")
	}

	endpoint := os.Getenv("OBSERVER_ENDPOINT")
	if endpoint == "" {
		endpoint = "observer.systemiq.ai:443"
	}

	maxMsg := 4 << 20 // 4 MiB default
	if v := os.Getenv("OBSERVER_MAX_MSG_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			maxMsg = mb << 20
		}
	}

	/* ---------- auth ---------- */
	authHandler, err := auth.NewAuthHandler()
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}

	/* ---------- dial Observer once ---------- */
	conn, client, err := dialObserver(endpoint)
	if err != nil {
		log.Fatalf("dial Observer: %v", err)
	}
	defer conn.Close()

	/* ---------- start local gRPC server ---------- */
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsg),
		grpc.MaxSendMsgSize(maxMsg),
	)

	protos.RegisterDataObserverServer(grpcServer, &ObserverMiddlewareServer{
		client:      client,
		authHandler: authHandler,
	})

	log.Println("ObserverMiddleware gRPC server is listening on port 50051...")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
