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

// Package cleaner provides utilities for cleaning up Kubernetes resources and files.
package cleaner

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

const (
	filesToDelete     = "filesToDelete"
	resourcesToDelete = "resourcesToDelete"
)

var (
	notif             = new(chan bool)
	propagationPolicy = metav1.DeletePropagationBackground

	// ErrIllegalCleanupNotification is returned when cleanup is notified before resources are cleaned.
	ErrIllegalCleanupNotification = errors.New("illegally notified cleanup prior to cleanup resources call")

	clusterRoleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	roleGVR               = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	roleBindingGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	namespaceGVR          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	serviceAccountGVR     = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
)

// DeleteObj is a struct that represents a Kubernetes resource to be deleted.
type DeleteObj struct {
	schema.GroupVersionResource

	// Name is the name of the resource to be deleted. Omit to delete all resources of this GVR.
	Name string `json:"name,omitempty"`

	// Namespace is the namespace of the resource to be deleted. Omit when deleting all resources for this GVR
	// across all namespaces, or when deleting a single cluster-scoped resource.
	Namespace string `json:"namespace,omitempty"`

	// MustDelete is a flag that indicates if the resource must be deleted.
	// If true, the cleanup will fail if the resource(s) are not deleted.
	// If false, the cleanup will continue even if the resource(s) are not deleted.
	MustDelete bool `json:"mustDelete,omitempty"`
}

// Cleaner is responsible for cleaning up resources and files.
type Cleaner struct {
	Debug                  bool
	CleanupTimeout         time.Duration
	DeletionInterval       time.Duration
	DeletionTimeout        time.Duration
	BlockingDeletion       bool
	EnableGRPCServer       bool
	GRPCPort               int
	FileConfigPath         string
	ResourceConfigPath     string
	SAName                 string
	RoleName               string
	RoleBindingName        string
	ClusterRoleName        string
	ClusterRoleBindingName string
}

// UseClusterRole returns true if both cluster role and cluster role binding are set.
func (c *Cleaner) UseClusterRole() bool {
	return c.ClusterRoleName != "" && c.ClusterRoleBindingName != ""
}

// readConfig loads a configuration file from the local filesystem
func readConfig(path, configType string) ([]byte, error) {
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
		return nil, nil
	} else if err != nil {
		log.Error().Err(err).Msg("failed to read config file")
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	return bytes, nil
}

// CleanupFiles deletes all files specified in the file cleanup config file.
func (c *Cleaner) CleanupFiles() error {
	files := []string{}
	bytes, err := readConfig(c.FileConfigPath, filesToDelete)
	if err != nil {
		return err
	}
	if bytes == nil {
		return nil
	}
	if err := json.Unmarshal(bytes, &files); err != nil {
		log.Error().Err(err).Msg("failed to unmarshal file cleanup config")
		return fmt.Errorf("failed to unmarshal file cleanup config: %w", err)
	}

	for _, filePath := range files {
		log.Info().Str("path", filePath).Msg("Deleting file")
		if err := os.Remove(filePath); err != nil {
			log.Error().Err(err).Msg("file deletion failed")
			continue
		}
		log.Info().Msg("File deletion successful")
	}
	return nil
}

// CleanupResources deletes all K8s resources specified in the resource cleanup config file.
func (c *Cleaner) CleanupResources(ctx context.Context, dc dynamic.Interface) error {
	resources := []DeleteObj{}
	bytes, err := readConfig(c.ResourceConfigPath, resourcesToDelete)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(bytes, &resources); err != nil {
		log.Error().Err(err).Msg("failed to unmarshal resource cleanup config")
		return fmt.Errorf("failed to unmarshal resource cleanup config: %w", err)
	}

	*notif = make(chan bool)

	numObjs := len(resources)
	for i, obj := range resources {
		// the final object in the resource config must be the spectro-cleanup Pod/DaemonSet/Job
		if i == numObjs-1 {
			if err := c.setOwnerReferences(ctx, dc, obj); err != nil {
				return err
			}

			// If BlockingDeletion is true, we've already waited for all resources to be deleted,
			// therefore we can self destruct immediately.
			if c.BlockingDeletion {
				log.Info().Msg("Self destructing...")
			} else {
				log.Info().
					Str("maxDelaySeconds", fmt.Sprintf("%.0f", c.CleanupTimeout.Seconds())).
					Msg("Waiting for final cleanup notification or timeout before destructing...")
				select {
				case <-*notif:
					log.Info().Msg("FinalizeCleanup notification received, self destructing...")
				case <-time.After(c.CleanupTimeout):
					log.Info().Msg(fmt.Sprintf("%.0f seconds elapsed, self destructing...", c.CleanupTimeout.Seconds()))
				}
			}
		}

		var err error
		if obj.Name == "" {
			err = c.deleteAllResources(ctx, dc, obj)
		} else {
			log.Info().
				Str("gvr", obj.GroupVersionResource.String()).
				Str("name", obj.Name).
				Str("namespace", obj.Namespace).
				Msg("deleting resource")
			err = c.deleteSingleResource(ctx, dc, obj)
		}
		if err != nil && obj.MustDelete {
			log.Error().
				Err(err).
				Str("gvr", obj.GroupVersionResource.String()).
				Msg("resource deletion failed")
			return fmt.Errorf("resource deletion failed: %w", err)
		}
	}

	close(*notif)
	*notif = nil
	return nil
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
			log.Error().Err(err).Msg("resource deletion failed after retries")
			return fmt.Errorf("resource deletion failed after retries: %w", err)
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

	return c.deleteResource(ctx, dc, obj, obj.Name, obj.Namespace, c.BlockingDeletion)
}

// deleteAllResources handles deletion of all resources of a given GVR.
// If a namespace is specified, only resources in that namespace will be deleted.
// If a namespace is not specified, all resources in all namespaces will be deleted.
func (c *Cleaner) deleteAllResources(ctx context.Context, dc dynamic.Interface, obj DeleteObj) error {
	log.Info().
		Str("gvr", obj.GroupVersionResource.String()).
		Str("namespace", obj.Namespace).
		Msg("deleting all resources of type")

	resources := unstructured.UnstructuredList{}

	namespaces, err := dc.Resource(namespaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to list namespaces")
		return err
	}
	for _, namespace := range namespaces.Items {
		ns := namespace.GetName()
		if obj.Namespace != "" && obj.Namespace != ns {
			log.Info().
				Str("gvr", obj.GroupVersionResource.String()).
				Str("namespace", ns).
				Msg("skipping namespace")
			continue
		}
		list, err := dc.Resource(obj.GroupVersionResource).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Error().Err(err).Msg("failed to list resources")
			return err
		}
		resources.Items = append(resources.Items, list.Items...)
	}
	if len(resources.Items) == 0 {
		log.Warn().
			Str("gvr", obj.GroupVersionResource.String()).
			Str("namespace", obj.Namespace).
			Msg("no resources found, skipping")
		return nil
	}

	if c.BlockingDeletion {
		return c.deleteAllResourcesBlocking(ctx, dc, obj, resources.Items)
	}
	return c.deleteAllResourcesNonBlocking(ctx, dc, obj, resources.Items)
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
func (c *Cleaner) setOwnerReferences(ctx context.Context, dc dynamic.Interface, obj DeleteObj) error {
	owner, err := dc.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to get resource")
		return fmt.Errorf("failed to get resource: %w", err)
	}
	ownerRef := metav1.OwnerReference{
		APIVersion: owner.GetAPIVersion(),
		Kind:       owner.GetKind(),
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
	}

	saKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.SAName}
	if err := c.setOwnerReferenceForResource(ctx, dc, saKey, ownerRef, serviceAccountGVR); err != nil {
		return err
	}

	if c.UseClusterRole() {
		clusterRoleKey := types.NamespacedName{Name: c.ClusterRoleName}
		if err := c.setOwnerReferenceForResource(ctx, dc, clusterRoleKey, ownerRef, clusterRoleGVR); err != nil {
			return err
		}

		clusterRoleBindingKey := types.NamespacedName{Name: c.ClusterRoleBindingName}
		if err := c.setOwnerReferenceForResource(ctx, dc, clusterRoleBindingKey, ownerRef, clusterRoleBindingGVR); err != nil {
			return err
		}
	} else {
		roleKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.RoleName}
		if err := c.setOwnerReferenceForResource(ctx, dc, roleKey, ownerRef, roleGVR); err != nil {
			return err
		}

		roleBindingKey := types.NamespacedName{Namespace: obj.Namespace, Name: c.RoleBindingName}
		if err := c.setOwnerReferenceForResource(ctx, dc, roleBindingKey, ownerRef, roleBindingGVR); err != nil {
			return err
		}
	}
	return nil
}

// setOwnerReferenceForResource is a helper function to set an owner reference on a Kubernetes resource.
func (c *Cleaner) setOwnerReferenceForResource(ctx context.Context, dc dynamic.Interface, key types.NamespacedName, ownerRef metav1.OwnerReference, gvr schema.GroupVersionResource) error {
	resource, err := dc.Resource(gvr).Namespace(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to get resource")
		return fmt.Errorf("failed to get resource: %w", err)
	}

	ownerReferences := resource.GetOwnerReferences()
	ownerReferences = append(ownerReferences, ownerRef)
	resource.SetOwnerReferences(ownerReferences)

	_, err = dc.Resource(gvr).Namespace(key.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		log.Error().Err(err).Msg("failed to update resource with owner reference")
		return fmt.Errorf("failed to update resource with owner reference: %w", err)
	}

	log.Info().Str(gvr.Resource, key.Name).Msg("Set cleanup ownerReference")
	return nil
}

func (c *Cleaner) waitForDeletion(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, c.DeletionInterval, c.DeletionTimeout, true, func(context.Context) (bool, error) {
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
		l.Str("retryInterval", c.DeletionInterval.String()).
			Str("retryTimeout", c.DeletionTimeout.String()).
			Msg("Resource not deleted")
		return false, nil
	})
}

// StartGRPCServer starts a gRPC server for FinalizeCleanup requests.
func (c *Cleaner) StartGRPCServer(wg *sync.WaitGroup) {
	defer wg.Done()

	mux := http.NewServeMux()
	path, handler := cleanupv1connect.NewCleanupServiceHandler(&cleanupServiceServer{})
	mux.Handle(path, handler)
	address := fmt.Sprintf("0.0.0.0:%d", c.GRPCPort)
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
func (s *cleanupServiceServer) FinalizeCleanup(_ context.Context, _ *connect.Request[cleanv1.FinalizeCleanupRequest]) (*connect.Response[cleanv1.FinalizeCleanupResponse], error) {
	log.Info().Msg("Received request to FinalizeCleanup")

	if *notif == nil {
		err := ErrIllegalCleanupNotification
		log.Error().Err(err).Msg("nil notification channel")
		return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), err
	}

	*notif <- true
	return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), nil
}
