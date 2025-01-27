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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"buf.build/gen/go/spectrocloud/spectro-cleanup/connectrpc/go/cleanup/v1/cleanupv1connect"
	cleanv1 "buf.build/gen/go/spectrocloud/spectro-cleanup/protocolbuffers/go/cleanup/v1"

	connect "connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	FilesToDelete     = "filesToDelete"
	ResourcesToDelete = "resourcesToDelete"
)

var (
	scheme            = runtime.NewScheme()
	log               = ctrl.Log.WithName("spectro-cleanup")
	notif             = new(chan bool)
	propagationPolicy = metav1.DeletePropagationBackground

	ErrIllegalCleanupNotification = errors.New("illegally notified cleanup prior to cleanup resources call")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
}

type Cleaner struct {
	cleanupTimeout     time.Duration
	deletionInterval   time.Duration
	deletionTimeout    time.Duration
	blockingDeletion   bool
	enableGrpcServer   bool
	grpcPort           int
	fileConfigPath     string
	resourceConfigPath string
	saName             string
	roleName           string
	roleBindingName    string
}

type DeleteObj struct {
	schema.GroupVersionResource
	Name      string
	Namespace string
}

func main() {
	var (
		cleanupTimeoutSeconds   int
		deletionIntervalSeconds int
		deletionTimeoutSeconds  int
	)

	c := &Cleaner{
		blockingDeletion:   true,
		grpcPort:           8080,
		fileConfigPath:     "/tmp/spectro-cleanup/file-config.json",
		resourceConfigPath: "/tmp/spectro-cleanup/resource-config.json",
		saName:             "spectro-cleanup",
		roleName:           "spectro-cleanup-role",
		roleBindingName:    "spectro-cleanup-rolebinding",
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

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	c.cleanupTimeout = time.Duration(cleanupTimeoutSeconds) * time.Second
	c.deletionInterval = time.Duration(deletionIntervalSeconds) * time.Second
	c.deletionTimeout = time.Duration(deletionTimeoutSeconds) * time.Second

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	ctx := context.Background()

	var wg sync.WaitGroup
	if c.enableGrpcServer {
		wg.Add(1)
		go c.startGRPCServer(&wg)
	}

	config := ctrl.GetConfigOrDie()
	client, err := ctrlclient.New(config, ctrlclient.Options{
		Scheme: scheme,
	})
	if err != nil {
		panic(err)
	}
	dynamic := dynamic.NewForConfigOrDie(config)

	c.cleanupFiles()
	c.cleanupResources(ctx, client, dynamic)

	wg.Wait()
	os.Exit(0)
}

// readConfig loads a configuration file from the local filesystem
func readConfig(path, configType string) []byte {
	path = filepath.Clean(path)
	log.Info("Reading Spectro Cleanup config", "path", path, "configType", configType)
	bytes, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		log.Info("WARNING: config file not found. Skipping.", "configType", configType)
		return nil
	} else if err != nil {
		panic(err)
	}
	return bytes
}

// cleanupFiles deletes all files specified in the file cleanup config file
func (c *Cleaner) cleanupFiles() {
	filesToDelete := []string{}
	bytes := readConfig(c.fileConfigPath, FilesToDelete)
	if bytes == nil {
		return
	}
	if err := json.Unmarshal(bytes, &filesToDelete); err != nil {
		panic(err)
	}

	for _, filePath := range filesToDelete {
		log.Info("Deleting file", "path", filePath)
		if err := os.Remove(filePath); err != nil {
			log.Error(err, "file deletion failed")
			continue
		}
		log.Info("File deletion successful")
	}
}

// cleanupResources deletes all K8s resources specified in the resource cleanup config file
func (c *Cleaner) cleanupResources(ctx context.Context, client ctrlclient.Client, dc dynamic.Interface) {
	resourcesToDelete := []DeleteObj{}
	bytes := readConfig(c.resourceConfigPath, ResourcesToDelete)
	if err := json.Unmarshal(bytes, &resourcesToDelete); err != nil {
		panic(err)
	}

	*notif = make(chan bool)

	numObjs := len(resourcesToDelete)
	for i, obj := range resourcesToDelete {
		// the final object in the resource config must be the spectro-cleanup Pod/DaemonSet/Job
		if i == numObjs-1 {
			c.setOwnerReferences(ctx, client, dc, obj)

			log.Info("Self destructing...", "maxDelaySeconds", c.cleanupTimeout)
			select {
			case <-*notif:
				log.Info("FinalizeCleanup notification received, self destructing")
			case <-time.After(c.cleanupTimeout):
				log.Info(fmt.Sprintf("%.0f seconds elapsed, self destructing", cleanupTimeout.Seconds()))
			}
		}

		gvrStr := obj.GroupVersionResource.String()
		log.Info("Deleting resource", "name", obj.Name, "namespace", obj.Namespace, "gvr", gvrStr)
		if err := dc.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).Delete(
			ctx, obj.Name, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy},
		); err != nil {
			log.Error(err, "resource deletion failed")
			continue
		}
		if c.blockingDeletion {
			if err := c.waitForDeletion(ctx, dc, obj.GroupVersionResource, obj.Namespace, obj.Name); err != nil {
				log.Error(err, "failed to verify resource deletion")
				continue
			}
		}
		log.Info("Resource deletion successful")
	}

	close(*notif)
	*notif = nil
}

// setOwnerReferences ensures garbage collection of RBAC resources used by cleanup Pod/DaemonSet/Job post self-destruction
func (c *Cleaner) setOwnerReferences(ctx context.Context, client ctrlclient.Client, dynamic dynamic.Interface, obj DeleteObj) {
	owner, err := dynamic.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		panic(err)
	}
	ownerRef := metav1.OwnerReference{
		APIVersion: owner.GetAPIVersion(),
		Kind:       owner.GetKind(),
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
	}

	sa := &corev1.ServiceAccount{}
	key := types.NamespacedName{Namespace: obj.Namespace, Name: c.saName}
	if err := client.Get(context.Background(), key, sa); err != nil {
		panic(err)
	}
	patch := ctrlclient.MergeFrom(sa.DeepCopy())
	sa.ObjectMeta.OwnerReferences = append(sa.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), sa, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "serviceAccount", c.saName)

	role := &rbacv1.Role{}
	key = types.NamespacedName{Namespace: obj.Namespace, Name: c.roleName}
	if err := client.Get(context.Background(), key, role); err != nil {
		panic(err)
	}
	patch = ctrlclient.MergeFrom(role.DeepCopy())
	role.ObjectMeta.OwnerReferences = append(role.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), role, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "role", c.roleName)

	rb := &rbacv1.RoleBinding{}
	key = types.NamespacedName{Namespace: obj.Namespace, Name: c.roleBindingName}
	if err := client.Get(context.Background(), key, rb); err != nil {
		panic(err)
	}
	patch = ctrlclient.MergeFrom(rb.DeepCopy())
	rb.ObjectMeta.OwnerReferences = append(rb.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), rb, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "roleBinding", c.roleBindingName)
}

func (c *Cleaner) waitForDeletion(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, c.deletionInterval, c.deletionTimeout, true, func(context.Context) (bool, error) {
		_, err := dc.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
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
		log.Info("gRPC server starting...", "address", address)
		err := server.ListenAndServe()
		if err != nil {
			log.Error(err, "gRPC server stopped, unable to handle further FinalizeCleanup requests")
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error(err, "Error while shutting down gRPC server")
		return
	}

	log.Info("gRPC server gracefully shut down")
}

// cleanupServiceServer implements the CleanupService API.
type cleanupServiceServer struct {
	cleanupv1connect.UnimplementedCleanupServiceHandler
}

// FinalizeCleanup notifies spectro-cleanup that it can now self destruct.
func (s *cleanupServiceServer) FinalizeCleanup(ctx context.Context, req *connect.Request[cleanv1.FinalizeCleanupRequest]) (*connect.Response[cleanv1.FinalizeCleanupResponse], error) {
	log.Info("Received request to FinalizeCleanup")

	if *notif == nil {
		err := ErrIllegalCleanupNotification
		log.Error(err, "nil notification channel")
		return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), err
	}

	*notif <- true
	return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), nil
}
