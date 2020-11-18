// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

/*
Package api implements the agent IPC api. Using HTTP
calls, it's possible to communicate with the agent,
sending commands and receiving infos.
*/
package api

import (
	"context"
	"crypto/tls"
	"fmt"
	stdLog "log"
	"net"
	"net/http"
	"strings"
	"time"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/DataDog/datadog-agent/cmd/agent/api/agent"
	"github.com/DataDog/datadog-agent/cmd/agent/api/check"
	pb "github.com/DataDog/datadog-agent/cmd/agent/api/pb"
	"github.com/DataDog/datadog-agent/pkg/api/security"
	"github.com/DataDog/datadog-agent/pkg/config"
	gorilla "github.com/gorilla/mux"
)

var (
	listener net.Listener
)

// grpcHandlerFunc returns an http.Handler that delegates to grpcServer on incoming gRPC
// connections or otherHandler otherwise. Copied from cockroachdb.
func grpcHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// This is a partial recreation of gRPC's internal checks https://github.com/grpc/grpc-go/pull/514/files#diff-95e9a25b738459a2d3030e1e6fa2a718R61
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	})
}

// StartServer creates the router and starts the HTTP server
func StartServer() error {
	var err error

	hosts := []string{"127.0.0.1", "localhost", "::1"}
	tlsAddr, err := getIPCAddressPort()
	if err == nil {
		hosts = append(hosts, tlsAddr)
	}
	tlsKeyPair, tlsCertPool := security.InitializeTLS(hosts)

	// get the transport we're going to use under HTTP
	listener, err = getListener()
	if err != nil {
		// we use the listener to handle commands for the Agent, there's
		// no way we can recover from this error
		return fmt.Errorf("Unable to create the api server: %v", err)
	}

	err = security.CreateAndSetAuthToken()
	if err != nil {
		return err
	}

	// gRPC server
	mux := http.NewServeMux()
	opts := []grpc.ServerOption{
		grpc.Creds(credentials.NewClientTLSFromCert(tlsCertPool, tlsAddr)),
		grpc.StreamInterceptor(grpc_auth.StreamServerInterceptor(security.GrpcAuth)),
		grpc.UnaryInterceptor(grpc_auth.UnaryServerInterceptor(security.GrpcAuth)),
	}

	s := grpc.NewServer(opts...)
	pb.RegisterAgentServer(s, &server{})
	pb.RegisterAgentSecureServer(s, &serverSecure{})

	dcreds := credentials.NewTLS(&tls.Config{
		ServerName: tlsAddr,
		RootCAs:    tlsCertPool,
	})
	dopts := []grpc.DialOption{grpc.WithTransportCredentials(dcreds)}

	// starting grpc gateway
	ctx := context.Background()
	gwmux := runtime.NewServeMux()
	err = pb.RegisterAgentHandlerFromEndpoint(
		ctx, gwmux, tlsAddr, dopts)
	if err != nil {
		panic(err)
	}

	err = pb.RegisterAgentSecureHandlerFromEndpoint(
		ctx, gwmux, tlsAddr, dopts)
	if err != nil {
		panic(err)
	}

	// Setup multiplexer
	// create the REST HTTP router
	agentMux := gorilla.NewRouter()
	checkMux := gorilla.NewRouter()
	// Validate token for every request
	agentMux.Use(validateToken)
	checkMux.Use(validateToken)

	mux.Handle("/agent/", http.StripPrefix("/agent", agent.SetupHandlers(agentMux)))
	mux.Handle("/check/", http.StripPrefix("/check", check.SetupHandlers(checkMux)))
	mux.Handle("/", gwmux)

	srv := &http.Server{
		Addr:    tlsAddr,
		Handler: grpcHandlerFunc(s, mux),
		// Handler: grpcHandlerFunc(s, r),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*tlsKeyPair},
			NextProtos:   []string{"h2"},
		},
		ErrorLog: stdLog.New(&config.ErrorLogWriter{
			AdditionalDepth: 4, // Use a stack depth of 4 on top of the default one to get a relevant filename in the stdlib
		}, "Error from the agent http API server: ", 0), // log errors to seelog,
		WriteTimeout: config.Datadog.GetDuration("server_timeout") * time.Second,
	}

	tlsListener := tls.NewListener(listener, srv.TLSConfig)

	go srv.Serve(tlsListener) //nolint:errcheck
	return nil
}

// StopServer closes the connection and the server
// stops listening to new commands.
func StopServer() {
	if listener != nil {
		listener.Close()
	}
}

// ServerAddress retruns the server address.
func ServerAddress() *net.TCPAddr {
	return listener.Addr().(*net.TCPAddr)
}
