package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"systemiq.ai/auth"
	"systemiq.ai/protos"
)

var testMode bool

// reconnectObserver establishes a gRPC connection to the external Observer service
// and returns the connection and client.
func reconnectObserver() (*grpc.ClientConn, protos.DataObserverClient, error) {
	observerEndpoint := os.Getenv("OBSERVER_ENDPOINT")
	if observerEndpoint == "" {
		observerEndpoint = "observer.systemiq.ai:443" // Default value
	}

	var opts []grpc.DialOption
	if strings.HasSuffix(observerEndpoint, ":443") {
		log.Println("Using TLS for Observer connection")
		creds := credentials.NewTLS(nil)
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		log.Println("Using insecure connection for Observer (non-443 endpoint)")
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// Enable client side keepalive pings so the channel notices half open connections quickly
	ka := keepalive.ClientParameters{
		Time:                2 * time.Minute,  // send pings every 2 min if the connection is idle
		Timeout:             20 * time.Second, // wait 20 s for ping ack before considering the connection dead
		PermitWithoutStream: false,            // do NOT ping when there are no active RPC streams
	}
	opts = append(opts, grpc.WithKeepaliveParams(ka))

	cp := grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  1 * time.Second,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   30 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}
	opts = append(opts, grpc.WithConnectParams(cp))
	conn, err := grpc.NewClient(observerEndpoint, opts...)
	if err != nil {
		log.Printf("Failed to connect to external Observer service: %v", err)
		return nil, nil, err
	}

	// Accept both READY and IDLE (Dial succeeded but no RPC made yet).
	state := conn.GetState()
	switch state {
	case connectivity.Ready:
		// All good.
	case connectivity.Idle:
		// Kick it once; if it fails it will move to TRANSIENT_FAILURE quickly.
		conn.Connect()
		// Do not force readiness here; let the first RPC waitforready handle it.
	default:
		// Try one explicit wait cycle.
		conn.Connect()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !conn.WaitForStateChange(ctx, state) || conn.GetState() != connectivity.Ready {
			conn.Close()
			return nil, nil, fmt.Errorf("observer not reachable, state %v", conn.GetState())
		}
	}

	client := protos.NewDataObserverClient(conn)

	// Report the post‑dial state explicitly.
	switch conn.GetState() {
	case connectivity.Ready:
		log.Println("Observer channel READY")
	default:
		log.Printf("Observer dialled, initial connectivity state %v", conn.GetState())
	}

	return conn, client, nil
}

// ObserverMiddlewareServer acts as the gRPC server for incoming broadcasts.
type ObserverMiddlewareServer struct {
	protos.UnimplementedDataObserverServer
	client      protos.DataObserverClient
	authHandler *auth.AuthHandler
	conn        *grpc.ClientConn
	mu          sync.RWMutex
}

// ObserveData handles incoming data from internal broadcasts and forwards it to the external Observer.
func (s *ObserverMiddlewareServer) ObserveData(ctx context.Context, req *protos.ObservationRequest) (*protos.ObservationResponse, error) {
	// Add a short deadline so calls do not block forever if the Observer is unavailable
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Ensure we have the latest access token
	jwtToken, err := s.authHandler.GetToken()
	if err != nil {
		log.Printf("Failed to get access token: %v", err)
		return nil, err
	}

	// Return a stub success immediately in test mode (no outbound gRPC).
	if testMode {
		return &protos.ObservationResponse{Status: "success"}, nil
	}

	// Forward the request to the external Observer service with the fresh token
	forwardedReq := &protos.ObservationRequest{
		Data:      req.Data,
		Indicator: req.Indicator,
		ElementId: req.ElementId,
		Token:     &jwtToken,
		Action:    req.Action,
	}

	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	// Forward to the server Observer (wait‑for‑ready) and capture the response
	response, err := client.ObserveData(ctx, forwardedReq, grpc.WaitForReady(true))
	if err != nil && status.Code(err) == codes.Unavailable {
		// Channel not READY: attempt a lazy reconnect once.
		log.Println("Observer unavailable, trying lazy reconnect...")
		newConn, newClient, recErr := reconnectObserver()
		if recErr == nil {
			s.mu.Lock()
			oldConn := s.conn
			s.conn = newConn
			s.client = newClient
			s.mu.Unlock()
			if oldConn != nil {
				oldConn.Close()
			}
			// retry once
			response, err = newClient.ObserveData(ctx, forwardedReq, grpc.WaitForReady(true))
		}
	}
	if err != nil {
		log.Printf("Failed to forward data to external Observer: %v", err)
		return nil, err
	}

	if response.Status == "error" {
		log.Printf("Error response received from external Observer for element_id: %d", req.ElementId)
	}

	return response, nil
}

func main() {
	// Initialize AuthHandler
	authHandler, err := auth.NewAuthHandler()
	if err != nil {
		log.Fatalf("Failed to initialize AuthHandler: %v", err)
	}

	if val := os.Getenv("TEST_MODE"); strings.ToLower(val) == "true" || val == "1" {
		testMode = true
		log.Println("Running in TEST MODE – external Observer calls are skipped")
	}

	// Establish initial gRPC connection using reconnectObserver.
	// If the Observer service is down, keep retrying instead of exiting.
	var conn *grpc.ClientConn
	var externalClient protos.DataObserverClient
	for {
		var err error
		conn, externalClient, err = reconnectObserver()
		if err != nil {
			log.Printf("Initial connect to Observer failed: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		break
	}
	defer conn.Close()

	// Set up the gRPC server for incoming broadcasts
	listener, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("Failed to listen on port 50051: %v", err)
	}
	maxMsgSize := 4 << 20 // default to 4MB
	if envSize := os.Getenv("OBSERVER_MAX_MSG_SIZE_MB"); envSize != "" {
		if parsedSize, err := strconv.Atoi(envSize); err == nil {
			maxMsgSize = parsedSize << 20
		}
	}
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
	)

	// Create the ObserverMiddlewareServer with health check support
	server := &ObserverMiddlewareServer{
		client:      externalClient,
		authHandler: authHandler,
		conn:        conn,
	}

	// Register the ObserverMiddlewareServer with the gRPC server
	protos.RegisterDataObserverServer(grpcServer, server)

	log.Println("ObserverMiddleware gRPC server is listening on port 50051...")

	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to serve gRPC server: %v", err)
	}
}
