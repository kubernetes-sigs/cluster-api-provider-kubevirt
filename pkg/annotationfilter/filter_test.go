package annotationfilter

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestFilter(t *testing.T) {
	filter := New([]string{"example.com/exclude", "example.com/also-exclude"})
	emptyFilter := New(nil)
	excluded := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"example.com/exclude": "value"},
		},
	}
	otherExcluded := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"example.com/also-exclude": "value"},
		},
	}
	included := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"example.com/include": "value"},
		},
	}

	if !filter.Excludes(excluded) || !filter.Excludes(otherExcluded) {
		t.Fatal("expected configured annotations to be excluded")
	}
	if filter.Excludes(included) {
		t.Fatal("expected unconfigured annotation to be included")
	}
	if !emptyFilter.IsEmpty() {
		t.Fatal("expected filter without annotations to be empty")
	}

	predicates := filter.Predicate()
	if predicates.Create(event.CreateEvent{Object: excluded}) {
		t.Fatal("expected excluded create event to be filtered")
	}
	if !predicates.Update(event.UpdateEvent{ObjectOld: included, ObjectNew: included}) {
		t.Fatal("expected included update event to pass")
	}
	if predicates.Delete(event.DeleteEvent{Object: otherExcluded}) {
		t.Fatal("expected excluded delete event to be filtered")
	}
	if !predicates.Generic(event.GenericEvent{Object: included}) {
		t.Fatal("expected included generic event to pass")
	}
}
