package main

import (
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"frameworks/api_firehose/internal/grpc"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("decklog")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Decklog (Firehose API)")

	// Setup Kafka producer
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "frameworks")

	producer, err := kafka.NewKafkaProducer(brokers, clusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer")
	}
	defer func() {
		if err := producer.Close(); err != nil {
			logger.WithError(err).Error("Failed to close Kafka producer")
		}
	}()

	// Get TLS configuration
	certFile := config.GetEnv("TLS_CERT_FILE", "/etc/letsencrypt/live/decklog/fullchain.pem")
	keyFile := config.GetEnv("TLS_KEY_FILE", "/etc/letsencrypt/live/decklog/privkey.pem")
	allowInsecure := config.GetEnvBool("ALLOW_INSECURE", false)

	// Create gRPC server
	grpcServer, err := grpc.NewGRPCServer(producer, logger, certFile, keyFile, allowInsecure)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create gRPC server")
	}

	// Start gRPC server
	port := config.GetEnv("GRPC_PORT", "18006")
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.WithError(err).Fatal("Failed to listen")
	}

	logger.WithField("port", port).Info("Starting gRPC server")

	// Handle graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("Shutting down gRPC server...")
		grpcServer.GracefulStop()
	}()

	// Start serving
	if err := grpcServer.Serve(lis); err != nil {
		logger.WithError(err).Fatal("Failed to serve")
	}
}
