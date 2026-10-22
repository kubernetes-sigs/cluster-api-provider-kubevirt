/*
Copyright 2026 The Kubernetes Authors.

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

package annotationfilter

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Filter matches objects carrying any configured annotation.
type Filter map[string]struct{}

// New returns a Filter for the supplied annotation names.
func New(names []string) Filter {
	filter := make(Filter, len(names))
	for _, name := range names {
		filter[name] = struct{}{}
	}
	return filter
}

// IsEmpty reports whether no annotations are configured for exclusion.
func (f Filter) IsEmpty() bool {
	return len(f) == 0
}

// Excludes reports whether object carries any configured annotation.
func (f Filter) Excludes(object metav1.Object) bool {
	if len(f) == 0 || object == nil {
		return false
	}
	annotations := object.GetAnnotations()
	for name := range f {
		if _, exists := annotations[name]; exists {
			return true
		}
	}
	return false
}

// Predicate returns controller-runtime event predicates that exclude objects carrying any configured annotation.
func (f Filter) Predicate() predicate.Funcs {
	return predicate.NewPredicateFuncs(func(object client.Object) bool {
		return !f.Excludes(object)
	})
}
