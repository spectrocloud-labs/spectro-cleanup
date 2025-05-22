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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"buf.build/gen/go/spectrocloud/spectro-cleanup/connectrpc/go/cleanup/v1/cleanupv1connect"
	cleanv1 "buf.build/gen/go/spectrocloud/spectro-cleanup/protocolbuffers/go/cleanup/v1"
	connect "connectrpc.com/connect"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

const (
	filesToDelete     = "filesToDelete"
	resourcesToDelete = "resourcesToDelete"
)

var (
	scheme            = runtime.NewScheme()
	notif             = new(chan bool)
	propagationPolicy = metav1.DeletePropagationBackground

	ErrIllegalCleanupNotification = errors.New("illegally notified cleanup prior to cleanup resources call")

	clusterRoleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	roleGVR               = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	roleBindingGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	serviceAccountGVR     = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
)

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Fatal().Err(err).Msg("failed to add client-go scheme")
	}
}

// Cleaner is responsible for cleaning up resources and files.
type Cleaner struct {
	debug                  bool
	cleanupTimeout         time.Duration
	deletionInterval       time.Duration
	deletionTimeout        time.Duration
	blockingDeletion       bool
	enableGrpcServer       bool
	grpcPort               int
	fileConfigPath         string
	resourceConfigPath     string
	saName                 string
	roleName               string
	roleBindingName        string
	clusterRoleName        string
	clusterRoleBindingName string
}

// DeleteObj is a struct that represents a Kubernetes resource to be deleted.
type DeleteObj struct {
	schema.GroupVersionResource

	// Name is the name of the resource to be deleted. Omit if DeleteAll is true.
	Name string `json:"name,omitempty"`

	// Namespace is the namespace of the resource to be deleted. Omit if DeleteAll is true.
	Namespace string `json:"namespace,omitempty"`

	// MustDelete is a flag that indicates if the resource must be deleted.
	// If true, the cleanup will fail if the resource(s) are not deleted.
	// If false, the cleanup will continue even if the resource(s) are not deleted.
	MustDelete bool `json:"mustDelete,omitempty"`

	// DeleteAll indicates whether to delete all resources of this GVR.
	// If true, Name and Namespace are ignored and all resources of this GVR will be deleted.
	DeleteAll bool `json:"deleteAll,omitempty"`
}

func main() {
	var (
		cleanupTimeoutSeconds   int
		deletionIntervalSeconds int
		deletionTimeoutSeconds  int
	)

	c := &Cleaner{
		blockingDeletion:       true,
		grpcPort:               8080,
		fileConfigPath:         "/tmp/spectro-cleanup/file-config.json",
		resourceConfigPath:     "/tmp/spectro-cleanup/resource-config.json",
		saName:                 "spectro-cleanup",
		roleName:               "spectro-cleanup-role",
		roleBindingName:        "spectro-cleanup-rolebinding",
		clusterRoleName:        "",
		clusterRoleBindingName: "",
	}

	flag.BoolVar(&c.blockingDeletion, "blocking-deletion", c.blockingDeletion, "Block until each resource is deleted before proceeding to the next")
	flag.IntVar(&deletionIntervalSeconds, "deletion-interval-seconds", 2, "Interval in seconds to poll for resource deletion")
	flag.IntVar(&deletionTimeoutSeconds, "deletion-timeout-seconds", 300, "Time in seconds to wait for resource deletion")

	flag.BoolVar(&c.enableGrpcServer, "enable-grpc-server", c.enableGrpcServer, "Enable gRPC server for FinalizeCleanup requests")
	flag.IntVar(&c.grpcPort, "grpc-port", c.grpcPort, "Port for gRPC server to listen on")
	flag.IntVar(&cleanupTimeoutSeconds, "cleanup-timeout", 30, "Time in seconds to wait before self-destructing")

	flag.StringVar(&c.saName, "sa-name", c.saName, "ServiceAccount name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.roleName, "role-name", c.roleName, "Role name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.roleBindingName, "role-binding-name", c.roleBindingName, "RoleBinding name for cleanup Pod/DaemonSet/Job")
	flag.StringVar(&c.clusterRoleName, "cluster-role-name", c.roleName, "ClusterRole name for cleanup Pod/DaemonSet/Job. If set, role-name will be ignored.")
	flag.StringVar(&c.clusterRoleBindingName, "cluster-role-binding-name", c.roleBindingName, "ClusterRoleBinding name for cleanup Pod/DaemonSet/Job. If set, role-binding-name will be ignored.")

	flag.BoolVar(&c.debug, "debug", c.debug, "Enable debug logging")

	if c.clusterRoleName == "" && c.clusterRoleBindingName != "" || c.clusterRoleName != "" && c.clusterRoleBindingName == "" {
		log.Fatal().Msg("cluster-role-name and cluster-role-binding-name must be set together")
	}

	flag.Parse()

	// Default level for this example is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if c.debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	c.cleanupTimeout = time.Duration(cleanupTimeoutSeconds) * time.Second
	c.deletionInterval = time.Duration(deletionIntervalSeconds) * time.Second
	c.deletionTimeout = time.Duration(deletionTimeoutSeconds) * time.Second

	log.Info().Msg("Starting spectro-cleanup")
	startTime := time.Now()

	ctx := context.Background()

	var wg sync.WaitGroup
	if c.enableGrpcServer {
		wg.Add(1)
		go c.startGRPCServer(&wg)
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

	c.cleanupFiles()
	log.Info().
		Str("duration", time.Since(startTime).String()).
		Msg("File cleanup complete")

	c.cleanupResources(ctx, dc)
	log.Info().
		Str("duration", time.Since(startTime).String()).
		Msg("Resource cleanup complete")

	wg.Wait()
	log.Info().
		Str("totalDuration", time.Since(startTime).String()).
		Msg("Cleanup finished")

	os.Exit(0)
}

func (c *Cleaner) useClusterRole() bool {
	return c.clusterRoleName != "" && c.clusterRoleBindingName != ""
}

// readConfig loads a configuration file from the local filesystem
func readConfig(path, configType string) []byte {
	path = filepath.Clean(path)
	log.Debug().
		Str("path", path).
		Str("configType", configType).
		Msg("Reading Spectro Cleanup config")
	bytes, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		log.Debug().
			Str("configType", configType).
			Msg("WARNING: config file not found. Skipping.")
		return nil
	} else if err != nil {
		log.Fatal().Err(err).Msg("failed to read config file")
	}
	return bytes
}

// cleanupFiles deletes all files specified in the file cleanup config file
func (c *Cleaner) cleanupFiles() {
	files := []string{}
	bytes := readConfig(c.fileConfigPath, filesToDelete)
	if bytes == nil {
		return
	}
	if err := json.Unmarshal(bytes, &files); err != nil {
		log.Fatal().Err(err).Msg("failed to unmarshal file cleanup config")
	}

	for _, filePath := range files {
		log.Info().Str("path", filePath).Msg("Deleting file")
		if err := os.Remove(filePath); err != nil {
			log.Error().Err(err).Msg("file deletion failed")
			continue
		}
		log.Info().Msg("File deletion successful")
	}
}

// deleteResource attempts to delete a single resource with retries
func (c *Cleaner) deleteResource(ctx context.Context, dc dynamic.Interface, obj DeleteObj, name, namespace string, waitForDeletion bool) error {
	deleteResource := func() error {
		err := dc.Resource(obj.GroupVersionResource).Namespace(namespace).Delete(
			ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy},
		)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Warn().Err(err).Msg("resource not found, skipping")
				return nil
			}
			log.Warn().Err(err).Msg("resource deletion failed")
			return err
		}
		return nil
	}

	// Retry delete operation
	err := retry.OnError(wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Cap:      30 * time.Second,
	}, retryable, deleteResource)
	if err != nil {
		if obj.MustDelete {
			log.Fatal().Err(err).Msg("resource deletion failed after retries")
		}
		log.Warn().Err(err).Msg("resource deletion failed after retries")
	}

	// Deletion has been initiated. If waitForDeletion is true, wait for the resource to be deleted.
	if waitForDeletion {
		if err := c.waitForDeletion(ctx, dc, obj.GroupVersionResource, namespace, name); err != nil {
			log.Error().Err(err).Msg("failed to verify resource deletion")
			return err
		}
	}

	return nil
}

// deleteSingleResource handles deletion of a single resource
func (c *Cleaner) deleteSingleResource(ctx context.Context, dc dynamic.Interface, obj DeleteObj) error {
	log.Info().
		Str("name", obj.Name).
		Str("namespace", obj.Namespace).
		Str("gvr", obj.GroupVersionResource.String()).
		Msg("Deleting resource")

	return c.deleteResource(ctx, dc, obj, obj.Name, obj.Namespace, c.blockingDeletion)
}

// deleteAllResources handles deletion of all resources of a given GVR
func (c *Cleaner) deleteAllResources(ctx context.Context, dc dynamic.Interface, obj DeleteObj) error {
	log.Info().
		Str("gvr", obj.GroupVersionResource.String()).
		Msg("Deleting all resources of type")

	list, err := dc.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to list resources")
		return err
	}

	if c.blockingDeletion {
		return c.deleteAllResourcesBlocking(ctx, dc, obj, list.Items)
	}
	return c.deleteAllResourcesNonBlocking(ctx, dc, obj, list.Items)
}

// deleteAllResourcesBlocking handles deletion of all resources with blocking behavior
func (c *Cleaner) deleteAllResourcesBlocking(ctx context.Context, dc dynamic.Interface, obj DeleteObj, items []unstructured.Unstructured) error {
	// First initiate all deletions in parallel
	if err := c.initiateParallelDeletions(ctx, dc, obj, items); err != nil {
		return err
	}

	// Then verify all deletions in parallel
	return c.verifyParallelDeletions(ctx, dc, obj, items)
}

// initiateParallelDeletions initiates deletion of all resources in parallel
func (c *Cleaner) initiateParallelDeletions(ctx context.Context, dc dynamic.Interface, obj DeleteObj, items []unstructured.Unstructured) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(items))

	for _, item := range items {
		wg.Add(1)
		go func(item *unstructured.Unstructured) {
			defer wg.Done()

			name := item.GetName()
			namespace := item.GetNamespace()
			if namespace == "" {
				namespace = obj.Namespace
			}

			log.Info().
				Str("name", name).
				Str("namespace", namespace).
				Str("gvr", obj.GroupVersionResource.String()).
				Msg("Deleting resource")

			// Don't wait for deletion here
			if err := c.deleteResource(ctx, dc, obj, name, namespace, false); err != nil {
				if obj.MustDelete {
					errChan <- fmt.Errorf("resource %s deletion failed: %w", name, err)
				}
			}
		}(&item)
	}

	wg.Wait()
	close(errChan)

	// Check if any errors occurred during deletion
	for err := range errChan {
		if obj.MustDelete {
			return err
		}
		log.Error().Err(err).Msg("resource deletion failed")
	}

	return nil
}

// verifyParallelDeletions verifies deletion of all resources in parallel
func (c *Cleaner) verifyParallelDeletions(ctx context.Context, dc dynamic.Interface, obj DeleteObj, items []unstructured.Unstructured) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(items))

	for _, item := range items {
		wg.Add(1)
		go func(item *unstructured.Unstructured) {
			defer wg.Done()

			name := item.GetName()
			namespace := item.GetNamespace()
			if namespace == "" {
				namespace = obj.Namespace
			}

			if err := c.waitForDeletion(ctx, dc, obj.GroupVersionResource, namespace, name); err != nil {
				if obj.MustDelete {
					errChan <- fmt.Errorf("failed to verify resource %s deletion: %w", name, err)
				}
				log.Error().Err(err).Msg("failed to verify resource deletion")
			}
		}(&item)
	}

	wg.Wait()
	close(errChan)

	// Check if any errors occurred during verification
	for err := range errChan {
		if obj.MustDelete {
			return err
		}
		log.Error().Err(err).Msg("resource deletion verification failed")
	}

	return nil
}

// deleteAllResourcesNonBlocking handles deletion of all resources without blocking
func (c *Cleaner) deleteAllResourcesNonBlocking(ctx context.Context, dc dynamic.Interface, obj DeleteObj, items []unstructured.Unstructured) error {
	for _, item := range items {
		name := item.GetName()
		namespace := item.GetNamespace()
		if namespace == "" {
			namespace = obj.Namespace
		}

		log.Info().
			Str("name", name).
			Str("namespace", namespace).
			Str("gvr", obj.GroupVersionResource.String()).
			Msg("Deleting resource")

		err := c.deleteResource(ctx, dc, obj, name, namespace, false)
		if err != nil && obj.MustDelete {
			return err
		}
	}

	return nil
}

// cleanupResources deletes all K8s resources specified in the resource cleanup config file
func (c *Cleaner) cleanupResources(ctx context.Context, dc dynamic.Interface) {
	resources := []DeleteObj{}
	bytes := readConfig(c.resourceConfigPath, resourcesToDelete)
	if err := json.Unmarshal(bytes, &resources); err != nil {
		log.Fatal().Err(err).Msg("failed to unmarshal resource cleanup config")
	}

	*notif = make(chan bool)

	numObjs := len(resources)
	for i, obj := range resources {
		// the final object in the resource config must be the spectro-cleanup Pod/DaemonSet/Job
		if i == numObjs-1 {
			c.setOwnerReferences(ctx, dc, obj)

			// If blockingDeletion is true, we've already waited for all resources to be deleted,
			// therefore we can self destruct immediately.
			if c.blockingDeletion {
				log.Info().Msg("Self destructing...")
			} else {
				log.Info().
					Str("maxDelaySeconds", fmt.Sprintf("%.0f", c.cleanupTimeout.Seconds())).
					Msg("Waiting for final cleanup notification or timeout before destructing...")
				select {
				case <-*notif:
					log.Info().Msg("FinalizeCleanup notification received, self destructing...")
				case <-time.After(c.cleanupTimeout):
					log.Info().Msg(fmt.Sprintf("%.0f seconds elapsed, self destructing...", c.cleanupTimeout.Seconds()))
				}
			}
		}

		var err error
		if obj.DeleteAll {
			err = c.deleteAllResources(ctx, dc, obj)
		} else {
			err = c.deleteSingleResource(ctx, dc, obj)
		}
		if err != nil && obj.MustDelete {
			log.Fatal().
				Err(err).
				Str("gvr", obj.GroupVersionResource.String()).
				Msg("resource deletion failed")
		}
	}

	close(*notif)
	*notif = nil
}

// retryable returns true to retry deletion requests on network errors and server errors
func retryable(err error) bool {
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		log.Debug().Str("error", err.Error()).Msg("Network error, retrying...")
		return true
	}
	if strings.Contains(err.Error(), "TLS handshake timeout") {
		log.Debug().Str("error", err.Error()).Msg("TLS handshake timeout, retrying...")
		return true
	}
	return false
}

// setOwnerReferences ensures garbage collection of RBAC resources used by cleanup Pod/DaemonSet/Job post self-destruction
func (c *Cleaner) setOwnerReferences(ctx context.Context, dc dynamic.Interface, obj DeleteObj) {
	owner, err := dc.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get resource")
	}
	ownerRef := metav1.OwnerReference{
		APIVersion: owner.GetAPIVersion(),
		Kind:       owner.GetKind(),
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
	}

	saKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.saName}
	c.setOwnerReferenceForResource(ctx, dc, saKey, ownerRef, serviceAccountGVR)

	if c.useClusterRole() {
		clusterRoleKey := types.NamespacedName{Name: c.clusterRoleName}
		c.setOwnerReferenceForResource(ctx, dc, clusterRoleKey, ownerRef, clusterRoleGVR)

		clusterRoleBindingKey := types.NamespacedName{Name: c.clusterRoleBindingName}
		c.setOwnerReferenceForResource(ctx, dc, clusterRoleBindingKey, ownerRef, clusterRoleBindingGVR)
	} else {
		roleKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.roleName}
		c.setOwnerReferenceForResource(ctx, dc, roleKey, ownerRef, roleGVR)

		roleBindingKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.roleBindingName}
		c.setOwnerReferenceForResource(ctx, dc, roleBindingKey, ownerRef, roleBindingGVR)
	}
}

// setOwnerReferenceForResource is a helper function to set an owner reference on a Kubernetes resource.
func (c *Cleaner) setOwnerReferenceForResource(ctx context.Context, dc dynamic.Interface, key types.NamespacedName, ownerRef metav1.OwnerReference, gvr schema.GroupVersionResource) {
	resource, err := dc.Resource(gvr).Namespace(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get resource")
	}

	ownerReferences := resource.GetOwnerReferences()
	ownerReferences = append(ownerReferences, ownerRef)
	resource.SetOwnerReferences(ownerReferences)

	_, err = dc.Resource(gvr).Namespace(key.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to update resource with owner reference")
	}

	log.Info().Str(gvr.Resource, key.Name).Msg("Set cleanup ownerReference")
}

func (c *Cleaner) waitForDeletion(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, c.deletionInterval, c.deletionTimeout, true, func(context.Context) (bool, error) {
		l := log.Info().
			Str("gvr", gvr.String()).
			Str("namespace", namespace).
			Str("name", name)
		_, err := dc.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				l.Msg("Resource deleted")
				return true, nil
			}
			return false, err
		}
		l.Str("retryInterval", c.deletionInterval.String()).
			Str("retryTimeout", c.deletionTimeout.String()).
			Msg("Resource not deleted")
		return false, nil
	})
}

func (c *Cleaner) startGRPCServer(wg *sync.WaitGroup) {
	defer wg.Done()

	mux := http.NewServeMux()
	path, handler := cleanupv1connect.NewCleanupServiceHandler(&cleanupServiceServer{})
	mux.Handle(path, handler)
	address := fmt.Sprintf("0.0.0.0:%d", c.grpcPort)
	server := &http.Server{
		Addr:         address,
		Handler:      h2c.NewHandler(mux, &http2.Server{}),
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	}
	go func() {
		log.Info().Str("address", address).Msg("gRPC server starting...")
		err := server.ListenAndServe()
		if err != nil {
			log.Error().Err(err).Msg("gRPC server stopped, unable to handle further FinalizeCleanup requests")
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("failed to shut down gRPC server")
		return
	}

	log.Info().Msg("gRPC server gracefully shut down")
}

// cleanupServiceServer implements the CleanupService API.
type cleanupServiceServer struct {
	cleanupv1connect.UnimplementedCleanupServiceHandler
}

// FinalizeCleanup notifies spectro-cleanup that it can now self destruct.
func (s *cleanupServiceServer) FinalizeCleanup(ctx context.Context, req *connect.Request[cleanv1.FinalizeCleanupRequest]) (*connect.Response[cleanv1.FinalizeCleanupResponse], error) {
	log.Info().Msg("Received request to FinalizeCleanup")

	if *notif == nil {
		err := ErrIllegalCleanupNotification
		log.Error().Err(err).Msg("nil notification channel")
		return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), err
	}

	*notif <- true
	return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), nil
}
