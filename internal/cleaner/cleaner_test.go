package cleaner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	cleanv1 "buf.build/gen/go/spectrocloud/spectro-cleanup/protocolbuffers/go/cleanup/v1"
	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/spectrocloud-labs/spectro-cleanup/internal/mock"
)

func TestReadConfig(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		expectedOutput []byte
		expectedError  bool
	}{
		{
			name:           "non existing file",
			path:           "tmp/nonexistingfile.json",
			expectedOutput: nil,
			expectedError:  false,
		},
		{
			name: "existing file",
			path: "/tmp/existingfile.json",
			expectedOutput: []byte(`[
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]`),
			expectedError: false,
		},
	}

	// Setup a temporary file for testing
	fileContent := []byte(`[
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]`)
	tmpFile, err := os.CreateTemp("", "existingfile.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(fileContent)
	tmpFile.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == "/tmp/existingfile.json" {
				tt.path = tmpFile.Name()
			}
			output, err := readConfig(tt.path, filesToDelete)
			if tt.expectedError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectedError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if string(output) != string(tt.expectedOutput) {
				t.Errorf("expected output %s, got %s", tt.expectedOutput, output)
			}
		})
	}
}

func TestCleanupResources(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		cleaner       *Cleaner
		resources     []DeleteObj
		mockSetup     func(*mock.DynamicClient)
		expectedError bool
	}{
		{
			name: "delete single resource: blocking, must delete, no error",
			cleaner: &Cleaner{
				BlockingDeletion:       true,
				DeletionInterval:       time.Second,
				DeletionTimeout:        time.Second * 5,
				SAName:                 "test-sa",
				ClusterRoleName:        "test-clusterrole",
				ClusterRoleBindingName: "test-clusterrolebinding",
			},
			resources: []DeleteObj{
				{
					GroupVersionResource: schema.GroupVersionResource{
						Group:    "test",
						Version:  "v1",
						Resource: "resources",
					},
					Name:       "test-resource",
					Namespace:  "test-ns",
					MustDelete: true,
				},
			},
			mockSetup: func(m *mock.DynamicClient) {
				m.GetFunc = func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
					if m.GetCallCount() == 1 {
						return &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "test/v1",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name":      name,
									"namespace": "test-ns",
									"uid":       "test-uid",
								},
							},
						}, nil
					}
					return nil, &apierrors.StatusError{
						ErrStatus: metav1.Status{
							Status:  metav1.StatusFailure,
							Code:    http.StatusNotFound,
							Reason:  metav1.StatusReasonNotFound,
							Message: "resource not found",
						},
					}
				}
			},
			expectedError: false,
		},
		{
			name: "delete single resource: non-blocking, must delete with error",
			cleaner: &Cleaner{
				BlockingDeletion:       false,
				DeletionInterval:       time.Second,
				DeletionTimeout:        time.Second * 5,
				SAName:                 "test-sa",
				ClusterRoleName:        "test-clusterrole",
				ClusterRoleBindingName: "test-clusterrolebinding",
			},
			resources: []DeleteObj{
				{
					GroupVersionResource: schema.GroupVersionResource{
						Group:    "test",
						Version:  "v1",
						Resource: "resources",
					},
					Name:       "test-resource",
					Namespace:  "test-ns",
					MustDelete: true,
				},
			},
			mockSetup: func(m *mock.DynamicClient) {
				m.GetFunc = func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
					if m.GetCallCount() == 1 {
						return &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "test/v1",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name":      name,
									"namespace": "test-ns",
									"uid":       "test-uid",
								},
							},
						}, nil
					}
					return nil, &apierrors.StatusError{
						ErrStatus: metav1.Status{
							Status:  metav1.StatusFailure,
							Code:    http.StatusNotFound,
							Reason:  metav1.StatusReasonNotFound,
							Message: "resource not found",
						},
					}
				}
				m.DeleteFunc = func(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
					return fmt.Errorf("delete failed")
				}
			},
			expectedError: true,
		},
		{
			name: "delete all resources in namespace",
			cleaner: &Cleaner{
				BlockingDeletion:       false,
				DeletionInterval:       time.Second,
				DeletionTimeout:        time.Second * 5,
				SAName:                 "test-sa",
				ClusterRoleName:        "test-clusterrole",
				ClusterRoleBindingName: "test-clusterrolebinding",
			},
			resources: []DeleteObj{
				{
					GroupVersionResource: schema.GroupVersionResource{
						Group:    "test",
						Version:  "v1",
						Resource: "resources",
					},
					Namespace:  "test-ns",
					MustDelete: true,
				},
			},
			mockSetup: func(m *mock.DynamicClient) {
				m.GetFunc = func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
					if m.GetCallCount() == 1 {
						return &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "test/v1",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name":      name,
									"namespace": "test-ns",
									"uid":       "test-uid",
								},
							},
						}, nil
					}
					return nil, &apierrors.StatusError{
						ErrStatus: metav1.Status{
							Status:  metav1.StatusFailure,
							Code:    http.StatusNotFound,
							Reason:  metav1.StatusReasonNotFound,
							Message: "resource not found",
						},
					}
				}
				m.RetList = &unstructured.UnstructuredList{
					Items: []unstructured.Unstructured{
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name": "test-ns",
								},
							},
						},
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name": "resource2",
								},
							},
						},
					},
				}
			},
			expectedError: false,
		},
		{
			name: "delete all resources across namespaces with blocking",
			cleaner: &Cleaner{
				BlockingDeletion: true,
				DeletionInterval: time.Second,
				DeletionTimeout:  time.Second * 5,
				SAName:           "test-sa",
				RoleName:         "test-role",
				RoleBindingName:  "test-rolebinding",
			},
			resources: []DeleteObj{
				{
					GroupVersionResource: schema.GroupVersionResource{
						Group:    "test",
						Version:  "v1",
						Resource: "resources",
					},
					MustDelete: true,
				},
			},
			mockSetup: func(m *mock.DynamicClient) {
				m.GetFunc = func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
					if m.GetCallCount() == 1 {
						return &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "test/v1",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name":      name,
									"namespace": "ns1",
								},
							},
						}, nil
					}
					return nil, &apierrors.StatusError{
						ErrStatus: metav1.Status{
							Status:  metav1.StatusFailure,
							Code:    http.StatusNotFound,
							Reason:  metav1.StatusReasonNotFound,
							Message: "resource not found",
						},
					}
				}
				m.RetList = &unstructured.UnstructuredList{
					Items: []unstructured.Unstructured{
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name":      "resource1",
									"namespace": "ns1",
								},
							},
						},
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name":      "resource2",
									"namespace": "ns2",
								},
							},
						},
					},
				}
			},
			expectedError: false,
		},
		{
			name: "delete all resources across namespaces without blocking",
			cleaner: &Cleaner{
				BlockingDeletion:       false,
				DeletionInterval:       time.Second,
				DeletionTimeout:        time.Second * 5,
				SAName:                 "test-sa",
				ClusterRoleName:        "test-clusterrole",
				ClusterRoleBindingName: "test-clusterrolebinding",
			},
			resources: []DeleteObj{
				{
					GroupVersionResource: schema.GroupVersionResource{
						Group:    "test",
						Version:  "v1",
						Resource: "resources",
					},
					MustDelete: true,
				},
			},
			mockSetup: func(m *mock.DynamicClient) {
				m.GetFunc = func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
					if m.GetCallCount() == 1 {
						return &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "test/v1",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name":      name,
									"namespace": "ns1",
								},
							},
						}, nil
					}
					return nil, &apierrors.StatusError{
						ErrStatus: metav1.Status{
							Status:  metav1.StatusFailure,
							Code:    http.StatusNotFound,
							Reason:  metav1.StatusReasonNotFound,
							Message: "resource not found",
						},
					}
				}
				m.RetList = &unstructured.UnstructuredList{
					Items: []unstructured.Unstructured{
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name":      "resource1",
									"namespace": "ns1",
								},
							},
						},
						{
							Object: map[string]interface{}{
								"metadata": map[string]interface{}{
									"name":      "resource2",
									"namespace": "ns2",
								},
							},
						},
					},
				}
			},
			expectedError: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Configure temporary config file
			tmpFile, err := os.CreateTemp("", "resources-*.json")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpFile.Name())
			configBytes, err := json.Marshal(tt.resources)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tmpFile.Name(), configBytes, 0644); err != nil {
				t.Fatal(err)
			}
			tt.cleaner.ResourceConfigPath = tmpFile.Name()

			// Setup mock client
			mockClient := mock.NewDynamicClient([]string{
				tt.cleaner.SAName,
				tt.cleaner.RoleName,
				tt.cleaner.RoleBindingName,
				tt.cleaner.ClusterRoleName,
				tt.cleaner.ClusterRoleBindingName,
			})
			tt.mockSetup(mockClient)

			// Setup mock REST mapper
			mockMapper := mock.NewRESTMapper()

			// Run the cleanup
			err = tt.cleaner.CleanupResources(ctx, mockClient, mockMapper)
			if (err != nil) != tt.expectedError {
				t.Errorf("expected error %v, got %v", tt.expectedError, err)
			}
		})
	}
}

func TestIsResourceClusterScoped(t *testing.T) {
	tests := []struct {
		name                  string
		gvr                   schema.GroupVersionResource
		expectedClusterScoped bool
		expectedError         bool
	}{
		{
			name: "namespaced resource (test group)",
			gvr: schema.GroupVersionResource{
				Group:    "test",
				Version:  "v1",
				Resource: "resources",
			},
			expectedClusterScoped: false,
			expectedError:         false,
		},
		{
			name: "cluster-scoped resource (core group)",
			gvr: schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "namespaces",
			},
			expectedClusterScoped: true,
			expectedError:         false,
		},
		{
			name: "cluster-scoped resource (rbac group)",
			gvr: schema.GroupVersionResource{
				Group:    "rbac.authorization.k8s.io",
				Version:  "v1",
				Resource: "clusterroles",
			},
			expectedClusterScoped: true,
			expectedError:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockMapper := mock.NewRESTMapper()

			isClusterScoped, err := isResourceClusterScoped(mockMapper, tt.gvr)

			if (err != nil) != tt.expectedError {
				t.Errorf("expected error %v, got %v", tt.expectedError, err)
			}

			if isClusterScoped != tt.expectedClusterScoped {
				t.Errorf("expected isClusterScoped=%v, got %v", tt.expectedClusterScoped, isClusterScoped)
			}
		})
	}
}

func TestParseGVR(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		expected    schema.GroupVersionResource
		expectedErr bool
	}{
		{
			name:     "named group",
			in:       "batch/v1/jobs",
			expected: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"},
		},
		{
			name:     "core group (empty leading segment)",
			in:       "/v1/configmaps",
			expected: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
		},
		{
			name:        "missing version",
			in:          "batch//jobs",
			expectedErr: true,
		},
		{
			name:        "missing resource",
			in:          "batch/v1/",
			expectedErr: true,
		},
		{
			name:        "too few segments",
			in:          "v1/jobs",
			expectedErr: true,
		},
		{
			name:        "too many segments",
			in:          "batch/v1/jobs/extra",
			expectedErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGVR(tt.in)
			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
			if !tt.expectedErr && got != tt.expected {
				t.Errorf("expected %+v, got %+v", tt.expected, got)
			}
		})
	}
}

func TestSelfCleanupTarget(t *testing.T) {
	tests := []struct {
		name        string
		cleaner     *Cleaner
		expectNil   bool
		expectedErr bool
		expected    *DeleteObj
	}{
		{
			name:      "disabled when SelfName empty",
			cleaner:   &Cleaner{SelfGVR: "batch/v1/jobs"},
			expectNil: true,
		},
		{
			name:        "invalid GVR returns error",
			cleaner:     &Cleaner{SelfName: "x", SelfGVR: "garbage"},
			expectedErr: true,
		},
		{
			name:    "valid namespaced target",
			cleaner: &Cleaner{SelfGVR: "batch/v1/jobs", SelfName: "mural-cleanup", SelfNamespace: "mural-system"},
			expected: &DeleteObj{
				GroupVersionResource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"},
				Name:                 "mural-cleanup",
				Namespace:            "mural-system",
				MustDelete:           true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cleaner.SelfCleanupTarget()
			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
			if tt.expectNil {
				if got != nil {
					t.Errorf("expected nil target, got %+v", got)
				}
				return
			}
			if tt.expected != nil && got != nil && *got != *tt.expected {
				t.Errorf("expected %+v, got %+v", *tt.expected, *got)
			}
		})
	}
}

// TestCleanupResources_SelfCleanupWithClusterScopedLastEntry covers the bug
// where a cluster-scoped resource as the last config entry caused
// setOwnerReferences to derive an empty SA namespace and fatal with
// "the server could not find the requested resource". With explicit self
// flags, the cleanup workload is identified independently of config order
// and self-cleanup proceeds against the correctly scoped target.
func TestCleanupResources_SelfCleanupWithClusterScopedLastEntry(t *testing.T) {
	ctx := context.Background()

	c := &Cleaner{
		BlockingDeletion:       false, // skip post-delete polling to keep the test fast
		DeletionInterval:       time.Millisecond * 10,
		DeletionTimeout:        time.Second,
		CleanupTimeout:         time.Millisecond * 50,
		SAName:                 "spectro-cleanup-sa",
		ClusterRoleName:        "spectro-cleanup-role",
		ClusterRoleBindingName: "spectro-cleanup-rolebinding",
		SelfGVR:                "batch/v1/jobs",
		SelfName:               "spectro-cleanup",
		SelfNamespace:          "kube-system",
	}

	resources := []DeleteObj{
		{
			// Cluster-scoped last entry: this is the bug condition.
			GroupVersionResource: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"},
			Name:                 "managed-cluster-set-spokes",
			MustDelete:           true,
		},
	}

	tmpFile, err := os.CreateTemp("", "resources-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	configBytes, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpFile.Name(), configBytes, 0644); err != nil {
		t.Fatal(err)
	}
	c.ResourceConfigPath = tmpFile.Name()

	// RBAC names always resolve via DefaultResource; the self-cleanup target
	// goes through GetFunc so we can assert it was Get'd in the correct namespace.
	mockClient := mock.NewDynamicClient([]string{
		c.SAName, c.ClusterRoleName, c.ClusterRoleBindingName,
	})
	mockClient.GetFunc = func(_ context.Context, name string, _ metav1.GetOptions, _ ...string) (*unstructured.Unstructured, error) {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "batch/v1",
				"kind":       "Job",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": "kube-system",
					"uid":       "test-uid",
				},
			},
		}, nil
	}

	if err := c.CleanupResources(ctx, mockClient, mock.NewRESTMapper()); err != nil {
		t.Fatalf("CleanupResources returned unexpected error: %v", err)
	}
}

// TestCleanupResources_NoSelfCleanup verifies that with SelfName unset,
// spectro-cleanup processes every config entry as a regular delete without
// any special last-entry handling, and runSelfCleanup is a no-op.
func TestCleanupResources_NoSelfCleanup(t *testing.T) {
	ctx := context.Background()

	c := &Cleaner{
		BlockingDeletion: false,
		DeletionInterval: time.Millisecond * 10,
		DeletionTimeout:  time.Second,
	}

	resources := []DeleteObj{
		{
			GroupVersionResource: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"},
			Name:                 "managed-cluster-set-spokes",
			MustDelete:           true,
		},
	}

	tmpFile, err := os.CreateTemp("", "resources-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	configBytes, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpFile.Name(), configBytes, 0644); err != nil {
		t.Fatal(err)
	}
	c.ResourceConfigPath = tmpFile.Name()

	mockClient := mock.NewDynamicClient(nil)
	// GetFunc would only be called if runSelfCleanup ran setOwnerReferences.
	getCalled := false
	mockClient.GetFunc = func(_ context.Context, _ string, _ metav1.GetOptions, _ ...string) (*unstructured.Unstructured, error) {
		getCalled = true
		return nil, nil
	}

	if err := c.CleanupResources(ctx, mockClient, mock.NewRESTMapper()); err != nil {
		t.Fatalf("CleanupResources returned unexpected error: %v", err)
	}
	if getCalled {
		t.Error("expected runSelfCleanup to be a no-op, but GetFunc was invoked")
	}
}

func TestFinalizeCleanup(t *testing.T) {
	server := &cleanupServiceServer{}
	ctx := context.TODO()
	req := connect.NewRequest(&cleanv1.FinalizeCleanupRequest{})

	tests := []struct {
		name        string
		testChan    chan bool
		expectedErr error
	}{
		{
			name:     "valid notification channel",
			testChan: make(chan bool),
		},
		{
			name:        "nil notification channel",
			testChan:    nil,
			expectedErr: ErrIllegalCleanupNotification,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notif = &tt.testChan

			go func() {
				<-time.After(1 * time.Second)
				<-tt.testChan
				close(tt.testChan)
			}()

			resp, err := server.FinalizeCleanup(ctx, req)
			if err != nil && tt.expectedErr == nil {
				t.Errorf("expected no error, got %v", err)
			}
			if err == nil && tt.expectedErr != nil {
				t.Errorf("expected error %v, got nil", tt.expectedErr)
			}
			if err != nil && tt.expectedErr != nil && err.Error() != tt.expectedErr.Error() {
				t.Errorf("expected error %v, got %v", tt.expectedErr, err)
			}
			if resp == nil {
				t.Errorf("expected response, got nil")
			}
		})
	}
}
