// Package mock provides a set of utilities for wrapping REST mappers
package mock

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RESTMapper is a mock implementation of meta.RESTMapper
type RESTMapper struct{}

// NewRESTMapper creates a new mock RESTMapper
func NewRESTMapper() *RESTMapper {
	return &RESTMapper{}
}

// KindFor returns a simple GVK based on the GVR
func (m *RESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{
		Group:   resource.Group,
		Version: resource.Version,
		Kind:    "Resource",
	}, nil
}

// KindsFor returns a list of potential kinds
func (m *RESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	gvk, err := m.KindFor(resource)
	if err != nil {
		return nil, err
	}
	return []schema.GroupVersionKind{gvk}, nil
}

// ResourceFor returns the input resource
func (m *RESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return input, nil
}

// ResourcesFor returns a list containing the input resource
func (m *RESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return []schema.GroupVersionResource{input}, nil
}

// RESTMapping returns a REST mapping for the provided group kind
// For testing purposes, this uses simplified scope detection:
//   - Resources in core group ("") or rbac group are treated as cluster-scoped (RESTScopeRoot)
//   - All other resources (like "test" group) are treated as namespaced (RESTScopeNamespace)
func (m *RESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	// Default to namespaced scope
	scope := meta.RESTScopeNamespace

	// Treat core and RBAC resources as cluster-scoped for testing
	if gk.Group == "" || gk.Group == "rbac.authorization.k8s.io" {
		scope = meta.RESTScopeRoot
	}

	return &meta.RESTMapping{
		Resource: schema.GroupVersionResource{
			Group:    gk.Group,
			Version:  versions[0],
			Resource: "resources",
		},
		GroupVersionKind: schema.GroupVersionKind{
			Group:   gk.Group,
			Version: versions[0],
			Kind:    gk.Kind,
		},
		Scope: scope,
	}, nil
}

// RESTMappings returns all resource mappings for the provided group kind
func (m *RESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	mapping, err := m.RESTMapping(gk, versions...)
	if err != nil {
		return nil, err
	}
	return []*meta.RESTMapping{mapping}, nil
}

// ResourceSingularizer returns a singular version of the resource name
func (m *RESTMapper) ResourceSingularizer(resource string) (string, error) {
	// Simple singularization for testing
	if len(resource) > 0 && resource[len(resource)-1] == 's' {
		return resource[:len(resource)-1], nil
	}
	return resource, nil
}
