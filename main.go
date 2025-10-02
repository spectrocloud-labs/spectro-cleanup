/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package main provides the entry point for the spectro-cleanup application.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/spectrocloud-labs/spectro-cleanup/internal/cleaner"
)

var scheme = runtime.NewScheme()

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Fatal().Err(err).Msg("failed to add client-go scheme")
	}
}

func main() {
	var (
		cleanupTimeoutSeconds   int
		deletionIntervalSeconds int
		deletionTimeoutSeconds  int
	)

	c := &cleaner.Cleaner{
		BlockingDeletion:       true,
		GRPCPort:               8080,
		FileConfigPath:         "/tmp/spectro-cleanup/file-config.json",
		ResourceConfigPath:     "/tmp/spectro-cleanup/resource-config.json",
		SAName:                 "spectro-cleanup",
		RoleName:               "spectro-cleanup-role",
		RoleBindingName:        "spectro-cleanup-rolebinding",
		ClusterRoleName:        "",
		ClusterRoleBindingName: "",
	}

	flag.BoolVar(&c.BlockingDeletion, "blocking-deletion", c.BlockingDeletion, "Block until each resource is deleted before proceeding to the next")
	flag.IntVar(&deletionIntervalSeconds, "deletion-interval-seconds", 2, "Interval in seconds to poll for resource deletion")
	flag.IntVar(&deletionTimeoutSeconds, "deletion-timeout-seconds", 300, "Time in seconds to wait for resource deletion")

	flag.BoolVar(&c.EnableGRPCServer, "enable-grpc-server", c.EnableGRPCServer, "Enable gRPC server for FinalizeCleanup requests")
	flag.IntVar(&c.GRPCPort, "grpc-port", c.GRPCPort, "Port for gRPC server to listen on")
	flag.IntVar(&cleanupTimeoutSeconds, "cleanup-timeout", 30, "Time in seconds to wait before self-destructing")

	flag.StringVar(&c.SAName, "sa-name", c.SAName, "ServiceAccount name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.RoleName, "role-name", c.RoleName, "Role name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.RoleBindingName, "role-binding-name", c.RoleBindingName, "RoleBinding name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.ClusterRoleName, "cluster-role-name", c.RoleName, "ClusterRole name for cleanup Pod/DaemonSet/Job. If set, role-name will be ignored.")
	flag.StringVar(&c.ClusterRoleBindingName, "cluster-role-binding-name", c.RoleBindingName, "ClusterRoleBinding name for cleanup Pod/DaemonSet/Job. If set, role-binding-name will be ignored.")

	flag.BoolVar(&c.Debug, "debug", c.Debug, "Enable debug logging")

	if c.ClusterRoleName == "" && c.ClusterRoleBindingName != "" || c.ClusterRoleName != "" && c.ClusterRoleBindingName == "" {
		log.Fatal().Msg("cluster-role-name and cluster-role-binding-name must be set together")
	}

	flag.Parse()

	// Default level for this example is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if c.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	c.CleanupTimeout = time.Duration(cleanupTimeoutSeconds) * time.Second
	c.DeletionInterval = time.Duration(deletionIntervalSeconds) * time.Second
	c.DeletionTimeout = time.Duration(deletionTimeoutSeconds) * time.Second

	log.Info().Msg("Starting spectro-cleanup")
	startTime := time.Now()

	ctx := context.Background()

	var wg sync.WaitGroup
	if c.EnableGRPCServer {
		wg.Add(1)
		go c.StartGRPCServer(&wg)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create in-cluster config")
	}

	// Increase timeouts for the HTTP client
	config.Timeout = time.Second * 30
	transport, err := rest.TransportFor(config)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create HTTP transport")
	}
	if httpTransport, ok := transport.(*http.Transport); ok {
		httpTransport.TLSHandshakeTimeout = time.Second * 15
		httpTransport.ResponseHeaderTimeout = time.Second * 30
		httpTransport.ExpectContinueTimeout = time.Second * 10
	}

	dc, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create dynamic client")
	}

	if err := c.CleanupFiles(); err != nil {
		log.Fatal().Err(err).Msg("failed to cleanup files")
	}
	log.Info().
		Str("duration", time.Since(startTime).String()).
		Msg("File cleanup complete")

	if err := c.CleanupResources(ctx, dc); err != nil {
		log.Fatal().Err(err).Msg("failed to cleanup resources")
	}
	log.Info().
		Str("duration", time.Since(startTime).String()).
		Msg("Resource cleanup complete")

	wg.Wait()
	log.Info().
		Str("totalDuration", time.Since(startTime).String()).
		Msg("Cleanup finished")

	os.Exit(0)
}
