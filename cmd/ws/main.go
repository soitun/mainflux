//
// Copyright (c) 2018
// Mainflux
//
// SPDX-License-Identifier: Apache-2.0
//

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	kitprometheus "github.com/go-kit/kit/metrics/prometheus"
	"github.com/mainflux/mainflux"
	"github.com/mainflux/mainflux/logger"
	thingsapi "github.com/mainflux/mainflux/things/api/grpc"
	adapter "github.com/mainflux/mainflux/ws"
	"github.com/mainflux/mainflux/ws/api"
	"github.com/mainflux/mainflux/ws/nats"
	broker "github.com/nats-io/go-nats"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	defClientTLS = "false"
	defCACerts   = ""
	defPort      = "8180"
	defLogLevel  = "error"
	defNatsURL   = broker.DefaultURL
	defThingsURL = "localhost:8181"
	envClientTLS = "MF_WS_ADAPTER_CLIENT_TLS"
	envCACerts   = "MF_WS_ADAPTER_CA_CERTS"
	envPort      = "MF_WS_ADAPTER_PORT"
	envLogLevel  = "MF_WS_ADAPTER_LOG_LEVEL"
	envNatsURL   = "MF_NATS_URL"
	envThingsURL = "MF_THINGS_URL"
)

type config struct {
	clientTLS bool
	caCerts   string
	thingsURL string
	natsURL   string
	logLevel  string
	port      string
}

func main() {
	cfg := loadConfig()

	logger, err := logger.New(os.Stdout, cfg.logLevel)
	if err != nil {
		log.Fatalf(err.Error())
	}

	nc, err := broker.Connect(cfg.natsURL)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to connect to NATS: %s", err))
		os.Exit(1)
	}
	defer nc.Close()

	conn := connectToThings(cfg, logger)
	defer conn.Close()

	cc := thingsapi.NewClient(conn)
	pubsub := nats.New(nc)
	svc := newService(pubsub, logger)

	errs := make(chan error, 2)

	go func() {
		p := fmt.Sprintf(":%s", cfg.port)
		logger.Info(fmt.Sprintf("WebSocket adapter service started, exposed port %s", cfg.port))
		errs <- http.ListenAndServe(p, api.MakeHandler(svc, cc, logger))
	}()

	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	err = <-errs
	logger.Error(fmt.Sprintf("WebSocket adapter terminated: %s", err))
}

func loadConfig() config {
	tls, err := strconv.ParseBool(mainflux.Env(envClientTLS, defClientTLS))
	if err != nil {
		tls = false
	}

	return config{
		clientTLS: tls,
		caCerts:   mainflux.Env(envCACerts, defCACerts),
		thingsURL: mainflux.Env(envThingsURL, defThingsURL),
		natsURL:   mainflux.Env(envNatsURL, defNatsURL),
		logLevel:  mainflux.Env(envLogLevel, defLogLevel),
		port:      mainflux.Env(envPort, defPort),
	}
}

func connectToThings(cfg config, logger logger.Logger) *grpc.ClientConn {
	var opts []grpc.DialOption
	if cfg.clientTLS {
		if cfg.caCerts != "" {
			tpc, err := credentials.NewClientTLSFromFile(cfg.caCerts, "")
			if err != nil {
				logger.Error(fmt.Sprintf("Failed to load certs: %s", err))
				os.Exit(1)
			}
			opts = append(opts, grpc.WithTransportCredentials(tpc))
		}
	} else {
		logger.Info("gRPC communication is not encrypted")
		opts = append(opts, grpc.WithInsecure())
	}

	conn, err := grpc.Dial(cfg.thingsURL, opts...)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to connect to things service: %s", err))
		os.Exit(1)
	}
	return conn
}

func newService(pubsub adapter.Service, logger logger.Logger) adapter.Service {
	svc := adapter.New(pubsub)
	svc = api.LoggingMiddleware(svc, logger)
	svc = api.MetricsMiddleware(
		svc,
		kitprometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: "ws_adapter",
			Subsystem: "api",
			Name:      "request_count",
			Help:      "Number of requests received.",
		}, []string{"method"}),
		kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
			Namespace: "ws_adapter",
			Subsystem: "api",
			Name:      "request_latency_microseconds",
			Help:      "Total duration of requests in microseconds.",
		}, []string{"method"}),
	)

	return svc
}
