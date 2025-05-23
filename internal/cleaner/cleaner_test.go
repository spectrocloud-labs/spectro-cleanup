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

			// Run the cleanup
			err = tt.cleaner.CleanupResources(ctx, mockClient)
			if (err != nil) != tt.expectedError {
				t.Errorf("expected error %v, got %v", tt.expectedError, err)
			}
		})
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
