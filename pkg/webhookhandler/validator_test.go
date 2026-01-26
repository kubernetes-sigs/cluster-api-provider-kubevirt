/*
Copyright 2021 The Kubernetes Authors.

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
	"errors"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

var _ = Describe("Template Validation - ensure immutability in update request", func() {
	Context("Template comparison without any errors", func() {
		oldTemplate := &v1alpha1.KubevirtMachineTemplate{
			Spec: v1alpha1.KubevirtMachineTemplateSpec{
				Template: v1alpha1.KubevirtMachineTemplateResource{
					Spec: v1alpha1.KubevirtMachineSpec{},
				},
			},
		}
		s := scheme.Scheme
		_ = v1alpha1.AddToScheme(s)
		wh := &kubevirtMachineTemplateHandler{decoder: admission.NewDecoder(s)}

		DescribeTable("check validator", func(newTemplate *v1alpha1.KubevirtMachineTemplate, expectError error) {
			err := wh.validateUpdate(oldTemplate, newTemplate)
			if expectError != nil {
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(expectError))
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
		},
			Entry("should not return error if there was no change", &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{},
					},
				},
			}, nil),
			Entry("should return error if there is a change", &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{ProviderID: ptr.To("testing")},
					},
				},
			}, errors.New(immutableWarning)),
		)
	})

	Context("check the validator webhook", func() {
		var (
			v1alpha1Codec runtime.Codec
			wh            *kubevirtMachineTemplateHandler
			ctx           context.Context
		)

		BeforeEach(func() {
			s := scheme.Scheme
			Expect(v1alpha1.AddToScheme(s)).To(Succeed())
			codecFactory := serializer.NewCodecFactory(s)
			v1alpha1Codec = codecFactory.LegacyCodec(v1alpha1.GroupVersion)
			wh = &kubevirtMachineTemplateHandler{decoder: admission.NewDecoder(s)}
			ctx = context.Background()
		})

		It("should always return OK for create request", func() {
			newTemplate := &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{},
					},
				},
			}

			req := newRequest(admissionv1.Create, newTemplate, nil, v1alpha1Codec)

			res := wh.Handle(ctx, req)
			Expect(res.Allowed).To(BeTrue())
			Expect(res.Result.Code).To(Equal(int32(http.StatusOK)))
		})

		It("should always return OK for delete request", func() {
			oldTemplate := &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{},
					},
				},
			}

			req := newRequest(admissionv1.Delete, oldTemplate, nil, v1alpha1Codec)

			res := wh.Handle(ctx, req)
			Expect(res.Allowed).To(BeTrue())
			Expect(res.Result.Code).To(Equal(int32(http.StatusOK)))
		})

		It("should return OK for update request, if there is no change", func() {
			oldTemplate := &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{},
					},
				},
			}

			newTemplate := oldTemplate.DeepCopy()

			req := newRequest(admissionv1.Update, oldTemplate, newTemplate, v1alpha1Codec)

			res := wh.Handle(ctx, req)
			Expect(res.Allowed).To(BeTrue())
			Expect(res.Result.Code).To(Equal(int32(http.StatusOK)))
		})

		It("should return error for update request, if there is a change", func() {
			oldTemplate := &v1alpha1.KubevirtMachineTemplate{
				Spec: v1alpha1.KubevirtMachineTemplateSpec{
					Template: v1alpha1.KubevirtMachineTemplateResource{
						Spec: v1alpha1.KubevirtMachineSpec{},
					},
				},
			}

			newTemplate := oldTemplate.DeepCopy()
			newTemplate.Spec.Template.Spec.ProviderID = ptr.To("testing")

			req := newRequest(admissionv1.Update, oldTemplate, newTemplate, v1alpha1Codec)

			res := wh.Handle(ctx, req)
			Expect(res.Allowed).To(BeFalse())
			Expect(res.Result.Code).To(Equal(int32(http.StatusForbidden)))
			Expect(res.Result.Message).To(Equal(immutableWarning))
		})
	})
})

func newRequest(operation admissionv1.Operation, oldObj, newObj *v1alpha1.KubevirtMachineTemplate, encoder runtime.Encoder) admission.Request {
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			Resource: metav1.GroupVersionResource{
				Group:    v1alpha1.GroupVersion.Group,
				Version:  v1alpha1.GroupVersion.Version,
				Resource: "testresource",
			},
			UID: "test-uid",
		},
	}

	switch operation {
	case admissionv1.Create:
		req.Object = runtime.RawExtension{
			Raw:    []byte(runtime.EncodeOrDie(encoder, oldObj)),
			Object: oldObj,
		}
	case admissionv1.Update:
		req.DryRun = ptr.To(false)
		req.Object = runtime.RawExtension{
			Raw:    []byte(runtime.EncodeOrDie(encoder, newObj)),
			Object: oldObj,
		}
		req.OldObject = runtime.RawExtension{
			Raw:    []byte(runtime.EncodeOrDie(encoder, oldObj)),
			Object: oldObj,
		}
	case admissionv1.Delete:
		req.OldObject = runtime.RawExtension{
			Raw:    []byte(runtime.EncodeOrDie(encoder, oldObj)),
			Object: oldObj,
		}
	default:
		req.Object = runtime.RawExtension{}
		req.OldObject = runtime.RawExtension{}
	}

	return req
}
