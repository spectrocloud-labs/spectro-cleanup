// Package mock provides a set of utilities for wrapping dynamic clients
package mock

import (
	"context"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

// DynamicClientWrapper is a mock interface for wrapping dynamic.DynamicClient
type DynamicClientWrapper interface {
	Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface
}

// DynamicClient is a mock implementation of dynamic.Interface
type DynamicClient struct {
	RetList      *unstructured.UnstructuredList
	DeleteFunc   func(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error
	GetFunc      func(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error)
	callCount    int32
	defaultNames map[string]bool
}

// NewDynamicClient creates a new DynamicClient with a list of names to return default values for
func NewDynamicClient(defaultNames []string) *DynamicClient {
	defaultNamesMap := make(map[string]bool)
	for _, name := range defaultNames {
		defaultNamesMap[name] = true
	}
	return &DynamicClient{
		defaultNames: defaultNamesMap,
	}
}

// DefaultResource checks if the given name matches any of the default names
func (m *DynamicClient) DefaultResource(name string) bool {
	return m.defaultNames[name]
}

// Resource ...
func (m *DynamicClient) Resource(_ schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return m
}

// Namespace ...
func (m *DynamicClient) Namespace(_ string) dynamic.ResourceInterface {
	return m
}

// Create ...
func (m *DynamicClient) Create(_ context.Context, _ *unstructured.Unstructured, _ metav1.CreateOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, nil
}

// Update ...
func (m *DynamicClient) Update(_ context.Context, _ *unstructured.Unstructured, _ metav1.UpdateOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, nil
}

// UpdateStatus ...
func (m *DynamicClient) UpdateStatus(_ context.Context, _ *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}

// Delete ...
func (m *DynamicClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, name, opts, subresources...)
	}
	return nil
}

// DeleteCollection ...
func (m *DynamicClient) DeleteCollection(_ context.Context, _ metav1.DeleteOptions, _ metav1.ListOptions) error {
	return nil
}

// Get ...
func (m *DynamicClient) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if m.DefaultResource(name) {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Resource",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": "test-ns",
					"uid":       "test-uid",
				},
			},
		}, nil
	}
	atomic.AddInt32(&m.callCount, 1)
	if m.GetFunc != nil {
		return m.GetFunc(ctx, name, opts, subresources...)
	}
	return nil, nil
}

// List ...
func (m *DynamicClient) List(_ context.Context, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return m.RetList, nil
}

// Watch ...
func (m *DynamicClient) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	return nil, nil
}

// Patch ...
func (m *DynamicClient) Patch(_ context.Context, _ string, _ types.PatchType, _ []byte, _ metav1.PatchOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, nil
}

// Apply ...
func (m *DynamicClient) Apply(_ context.Context, _ string, _ *unstructured.Unstructured, _ metav1.ApplyOptions, _ ...string) (*unstructured.Unstructured, error) {
	return nil, nil
}

// ApplyStatus ...
func (m *DynamicClient) ApplyStatus(_ context.Context, _ string, _ *unstructured.Unstructured, _ metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}

// GetCallCount returns the current call count
func (m *DynamicClient) GetCallCount() int32 {
	return atomic.LoadInt32(&m.callCount)
}

// ResetCallCount resets the call counter to 0
func (m *DynamicClient) ResetCallCount() {
	atomic.StoreInt32(&m.callCount, 0)
}
