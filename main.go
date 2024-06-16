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
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	FilesToDelete     = "filesToDelete"
	ResourcesToDelete = "resourcesToDelete"
)

var (
	scheme = runtime.NewScheme()
	log    = ctrl.Log.WithName("spectro-cleanup")
	notif  = new(chan bool)

	// optional env vars to override default configuration
	cleanupSeconds      int64
	enableGrpcServer    bool
	propagationPolicy   = metav1.DeletePropagationBackground
	cleanupSecondsStr   = os.Getenv("CLEANUP_DELAY_SECONDS")
	fileConfigPath      = os.Getenv("CLEANUP_FILE_CONFIG_PATH")
	resourceConfigPath  = os.Getenv("CLEANUP_RESOURCE_CONFIG_PATH")
	saName              = os.Getenv("CLEANUP_SA_NAME")
	roleName            = os.Getenv("CLEANUP_ROLE_NAME")
	roleBindingName     = os.Getenv("CLEANUP_ROLEBINDING_NAME")
	enableGrpcServerStr = os.Getenv("CLEANUP_GRPC_SERVER_ENABLED")
	grpcPortStr         = os.Getenv("CLEANUP_GRPC_SERVER_PORT")

	ErrIllegalCleanupNotification = errors.New("illegally notified cleanup prior to cleanup resources call")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	initConfig()
}

type DeleteObj struct {
	schema.GroupVersionResource
	Name      string
	Namespace string
}

func main() {
	ctrl.SetLogger(textlogger.NewLogger(textlogger.NewConfig()))
	ctx := context.TODO()

	var wg sync.WaitGroup
	if enableGrpcServer {
		wg.Add(1)
		go startGRPCServer(&wg)
	}

	config := ctrl.GetConfigOrDie()
	client, err := ctrlclient.New(config, ctrlclient.Options{
		Scheme: scheme,
	})
	if err != nil {
		panic(err)
	}
	dynamic := dynamic.NewForConfigOrDie(config)

	cleanupFiles()
	cleanupResources(ctx, client, dynamic)

	wg.Wait()
	os.Exit(0)
}

func initConfig() {
	// RBAC resources used to grant the spectro cleanup Pod/DaemonSet/Job
	// the privileges necessary to perform its cleanup
	if saName == "" {
		saName = "spectro-cleanup"
	}
	if roleName == "" {
		roleName = "spectro-cleanup-role"
	}
	if roleBindingName == "" {
		roleBindingName = "spectro-cleanup-rolebinding"
	}

	// Configuration files indicating which files and K8s resources to clean up
	if fileConfigPath == "" {
		fileConfigPath = "/tmp/spectro-cleanup/file-config.json"
	}
	if resourceConfigPath == "" {
		resourceConfigPath = "/tmp/spectro-cleanup/resource-config.json"
	}

	// How long the spectro cleanup Pod/DaemonSet/Job will wait before self-destructing
	if cleanupSecondsStr == "" {
		cleanupSeconds = 30
	} else {
		var err error
		cleanupSeconds, err = strconv.ParseInt(cleanupSecondsStr, 10, 64)
		if err != nil {
			panic(err)
		}
	}

	if enableGrpcServerStr == "true" {
		enableGrpcServer = true

		_, err := strconv.Atoi(grpcPortStr)
		if err != nil {
			panic(err)
		}
	}
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
func cleanupFiles() {
	filesToDelete := []string{}
	bytes := readConfig(fileConfigPath, FilesToDelete)
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
func cleanupResources(ctx context.Context, client ctrlclient.Client, dynamic dynamic.Interface) {
	resourcesToDelete := []DeleteObj{}
	bytes := readConfig(resourceConfigPath, ResourcesToDelete)
	if err := json.Unmarshal(bytes, &resourcesToDelete); err != nil {
		panic(err)
	}

	*notif = make(chan bool)

	numObjs := len(resourcesToDelete)
	for i, obj := range resourcesToDelete {
		// the final object in the resource config must be the spectro-cleanup Pod/DaemonSet/Job
		if i == numObjs-1 {
			setOwnerReferences(ctx, client, dynamic, obj)

			log.Info("Self destructing...", "maxDelaySeconds", cleanupSeconds)
			select {
			case <-*notif:
				log.Info("FinalizeCleanup notification received, self destructing")
			case <-time.After(time.Duration(cleanupSeconds) * time.Second):
				log.Info(fmt.Sprintf("%d seconds elapsed, self destructing", cleanupSeconds))
			}
		}

		gvrStr := obj.GroupVersionResource.String()
		log.Info("Deleting resource", "name", obj.Name, "namespace", obj.Namespace, "gvr", gvrStr)
		if err := dynamic.Resource(obj.GroupVersionResource).Namespace(obj.Namespace).Delete(
			ctx, obj.Name, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy},
		); err != nil {
			log.Error(err, "resource deletion failed")
			continue
		}
		log.Info("Resource deletion successful")
	}

	close(*notif)
	*notif = nil
}

// setOwnerReferences ensures garbage collection of RBAC resources used by cleanup Pod/DaemonSet/Job post self-destruction
func setOwnerReferences(ctx context.Context, client ctrlclient.Client, dynamic dynamic.Interface, obj DeleteObj) {
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
	key := types.NamespacedName{Namespace: obj.Namespace, Name: saName}
	if err := client.Get(context.Background(), key, sa); err != nil {
		panic(err)
	}
	patch := ctrlclient.MergeFrom(sa.DeepCopy())
	sa.ObjectMeta.OwnerReferences = append(sa.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), sa, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "serviceAccount", saName)

	role := &rbacv1.Role{}
	key = types.NamespacedName{Namespace: obj.Namespace, Name: roleName}
	if err := client.Get(context.Background(), key, role); err != nil {
		panic(err)
	}
	patch = ctrlclient.MergeFrom(role.DeepCopy())
	role.ObjectMeta.OwnerReferences = append(role.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), role, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "role", roleName)

	rb := &rbacv1.RoleBinding{}
	key = types.NamespacedName{Namespace: obj.Namespace, Name: roleBindingName}
	if err := client.Get(context.Background(), key, rb); err != nil {
		panic(err)
	}
	patch = ctrlclient.MergeFrom(rb.DeepCopy())
	rb.ObjectMeta.OwnerReferences = append(rb.ObjectMeta.OwnerReferences, ownerRef)
	if err := client.Patch(context.Background(), rb, patch); err != nil {
		panic(err)
	}
	log.Info("Set cleanup ownerReference", "roleBinding", roleBindingName)
}

func startGRPCServer(wg *sync.WaitGroup) {
	defer wg.Done()

	mux := http.NewServeMux()
	path, handler := cleanupv1connect.NewCleanupServiceHandler(&cleanupServiceServer{})
	mux.Handle(path, handler)
	address := fmt.Sprintf("0.0.0.0:%s", grpcPortStr)
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
func (s *cleanupServiceServer) FinalizeCleanup(
	ctx context.Context,
	req *connect.Request[cleanv1.FinalizeCleanupRequest],
) (*connect.Response[cleanv1.FinalizeCleanupResponse], error) {
	log.Info("Received request to FinalizeCleanup")
	if *notif == nil {
		err := ErrIllegalCleanupNotification
		log.Error(err, "nil notification channel")
		return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), err
	}

	*notif <- true
	return connect.NewResponse(&cleanv1.FinalizeCleanupResponse{}), nil
}
