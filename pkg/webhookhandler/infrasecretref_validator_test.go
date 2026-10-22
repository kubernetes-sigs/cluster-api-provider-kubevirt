/*
Copyright 2024 The Kubernetes Authors.

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
package webhookhandler

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/annotationfilter"
)

var _ = Describe("InfraClusterSecretRef Validation", func() {
	var (
		clusterCodec runtime.Codec
		machineCodec runtime.Codec
		validator    *infraSecretRefValidator
		ctx          context.Context
	)

	BeforeEach(func() {
		s := scheme.Scheme
		Expect(v1alpha1.AddToScheme(s)).To(Succeed())
		codecFactory := serializer.NewCodecFactory(s)
		clusterCodec = codecFactory.LegacyCodec(v1alpha1.GroupVersion)
		machineCodec = codecFactory.LegacyCodec(v1alpha1.GroupVersion)
		validator = &infraSecretRefValidator{
			decoder:             admission.NewDecoder(s),
			controllerNamespace: "capk-system",
			excludedAnnotations: annotationfilter.New([]string{"example.com/exclude"}),
		}
		ctx = context.Background()
	})

	Context("KubevirtCluster validation", func() {
		It("should allow create with no infraClusterSecretRef", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should allow create with infraClusterSecretRef in same namespace", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "my-secret",
						Namespace: "test-ns",
					},
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should allow create with infraClusterSecretRef with empty namespace", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name: "my-secret",
					},
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should reject create with infraClusterSecretRef in different namespace", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "different-ns",
					},
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeFalse())
			Expect(res.Result.Code).To(Equal(int32(http.StatusForbidden)))
			Expect(res.Result.Message).To(ContainSubstring("different-ns"))
			Expect(res.Result.Message).To(ContainSubstring("test-ns"))
		})

		It("should allow a cluster carrying an excluded annotation without validating the infra secret reference", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"example.com/exclude": "true",
					},
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "different-ns",
					},
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should reject update with infraClusterSecretRef in different namespace", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "another-ns",
					},
				},
			}
			req := newClusterRequest(admissionv1.Update, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeFalse())
			Expect(res.Result.Message).To(ContainSubstring("another-ns"))
		})

		It("should allow create with infraClusterSecretRef in controller namespace", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "infra-kubeconfig",
						Namespace: "capk-system",
					},
				},
			}
			req := newClusterRequest(admissionv1.Create, cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should allow delete without validation", func() {
			cluster := &v1alpha1.KubevirtCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtClusterSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "other-ns",
					},
				},
			}
			req := newClusterDeleteRequest(cluster, clusterCodec)
			res := validator.HandleCluster(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})
	})

	Context("KubevirtMachine validation", func() {
		It("should allow create with no infraClusterSecretRef", func() {
			machine := &v1alpha1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine",
					Namespace: "test-ns",
				},
			}
			req := newMachineRequest(admissionv1.Create, machine, machineCodec)
			res := validator.HandleMachine(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should allow create with infraClusterSecretRef in same namespace", func() {
			machine := &v1alpha1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtMachineSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "my-secret",
						Namespace: "test-ns",
					},
				},
			}
			req := newMachineRequest(admissionv1.Create, machine, machineCodec)
			res := validator.HandleMachine(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should reject create with infraClusterSecretRef in different namespace", func() {
			machine := &v1alpha1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtMachineSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "different-ns",
					},
				},
			}
			req := newMachineRequest(admissionv1.Create, machine, machineCodec)
			res := validator.HandleMachine(ctx, req)
			Expect(res.Allowed).To(BeFalse())
			Expect(res.Result.Code).To(Equal(int32(http.StatusForbidden)))
			Expect(res.Result.Message).To(ContainSubstring("different-ns"))
		})

		It("should allow a machine carrying an excluded annotation without validating the infra secret reference", func() {
			machine := &v1alpha1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"example.com/exclude": "true",
					},
				},
				Spec: v1alpha1.KubevirtMachineSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "different-ns",
					},
				},
			}
			req := newMachineRequest(admissionv1.Create, machine, machineCodec)
			res := validator.HandleMachine(ctx, req)
			Expect(res.Allowed).To(BeTrue())
		})

		It("should reject update with infraClusterSecretRef in different namespace", func() {
			machine := &v1alpha1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.KubevirtMachineSpec{
					InfraClusterSecretRef: &corev1.ObjectReference{
						Name:      "some-secret",
						Namespace: "another-ns",
					},
				},
			}
			req := newMachineRequest(admissionv1.Update, machine, machineCodec)
			res := validator.HandleMachine(ctx, req)
			Expect(res.Allowed).To(BeFalse())
			Expect(res.Result.Message).To(ContainSubstring("another-ns"))
		})
	})

	Context("validateInfraClusterSecretRef", func() {
		It("should return nil for nil ref", func() {
			Expect(validateInfraClusterSecretRef(nil, "any-ns", "ctrl-ns")).To(Succeed())
		})

		It("should return nil for empty namespace", func() {
			ref := &corev1.ObjectReference{Name: "secret"}
			Expect(validateInfraClusterSecretRef(ref, "any-ns", "ctrl-ns")).To(Succeed())
		})

		It("should return nil for matching namespace", func() {
			ref := &corev1.ObjectReference{Name: "secret", Namespace: "my-ns"}
			Expect(validateInfraClusterSecretRef(ref, "my-ns", "ctrl-ns")).To(Succeed())
		})

		It("should return nil for controller namespace", func() {
			ref := &corev1.ObjectReference{Name: "secret", Namespace: "ctrl-ns"}
			Expect(validateInfraClusterSecretRef(ref, "my-ns", "ctrl-ns")).To(Succeed())
		})

		It("should return error for unrelated namespace", func() {
			ref := &corev1.ObjectReference{Name: "secret", Namespace: "other-ns"}
			err := validateInfraClusterSecretRef(ref, "my-ns", "ctrl-ns")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("other-ns"))
			Expect(err.Error()).To(ContainSubstring("my-ns"))
		})

		It("should return error when controller namespace is empty and ref is cross-namespace", func() {
			ref := &corev1.ObjectReference{Name: "secret", Namespace: "other-ns"}
			err := validateInfraClusterSecretRef(ref, "my-ns", "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("other-ns"))
		})
	})
})

func newClusterRequest(operation admissionv1.Operation, obj *v1alpha1.KubevirtCluster, encoder runtime.Encoder) admission.Request {
	raw := runtime.RawExtension{
		Raw:    []byte(runtime.EncodeOrDie(encoder, obj)),
		Object: obj,
	}

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			Resource: metav1.GroupVersionResource{
				Group:    v1alpha1.GroupVersion.Group,
				Version:  v1alpha1.GroupVersion.Version,
				Resource: "kubevirtclusters",
			},
			Namespace: obj.Namespace,
			UID:       "test-uid",
			Object:    raw,
		},
	}

	return req
}

func newClusterDeleteRequest(obj *v1alpha1.KubevirtCluster, encoder runtime.Encoder) admission.Request {
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Delete,
			Resource: metav1.GroupVersionResource{
				Group:    v1alpha1.GroupVersion.Group,
				Version:  v1alpha1.GroupVersion.Version,
				Resource: "kubevirtclusters",
			},
			Namespace: obj.Namespace,
			UID:       "test-uid",
			OldObject: runtime.RawExtension{
				Raw:    []byte(runtime.EncodeOrDie(encoder, obj)),
				Object: obj,
			},
		},
	}
}

func newMachineRequest(operation admissionv1.Operation, obj *v1alpha1.KubevirtMachine, encoder runtime.Encoder) admission.Request {
	raw := runtime.RawExtension{
		Raw:    []byte(runtime.EncodeOrDie(encoder, obj)),
		Object: obj,
	}

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			Resource: metav1.GroupVersionResource{
				Group:    v1alpha1.GroupVersion.Group,
				Version:  v1alpha1.GroupVersion.Version,
				Resource: "kubevirtmachines",
			},
			Namespace: obj.Namespace,
			UID:       "test-uid",
			Object:    raw,
		},
	}

	return req
}
