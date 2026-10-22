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
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/annotationfilter"
)

const (
	kubevirtClusterValidationPath = "/validate-infrastructure-cluster-x-k8s-io-v1alpha1-kubevirtcluster"
	kubevirtMachineValidationPath = "/validate-infrastructure-cluster-x-k8s-io-v1alpha1-kubevirtmachine"
)

type infraSecretRefValidator struct {
	decoder             admission.Decoder
	controllerNamespace string
	excludedAnnotations annotationfilter.Filter
}

func (v *infraSecretRefValidator) HandleCluster(_ context.Context, req admission.Request) admission.Response {
	cluster := &v1alpha1.KubevirtCluster{}

	switch req.Operation {
	case admissionv1.Create, admissionv1.Update:
		if err := v.decoder.Decode(req, cluster); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if v.excludedAnnotations.Excludes(cluster) {
			return admission.Allowed("")
		}
		if err := validateInfraClusterSecretRef(cluster.Spec.InfraClusterSecretRef, cluster.Namespace, v.controllerNamespace); err != nil {
			return admission.Denied(err.Error())
		}
	case admissionv1.Delete:
		// no validation needed on delete
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation request %q", req.Operation))
	}

	return admission.Allowed("")
}

func (v *infraSecretRefValidator) HandleMachine(_ context.Context, req admission.Request) admission.Response {
	machine := &v1alpha1.KubevirtMachine{}

	switch req.Operation {
	case admissionv1.Create, admissionv1.Update:
		if err := v.decoder.Decode(req, machine); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if v.excludedAnnotations.Excludes(machine) {
			return admission.Allowed("")
		}
		if err := validateInfraClusterSecretRef(machine.Spec.InfraClusterSecretRef, machine.Namespace, v.controllerNamespace); err != nil {
			return admission.Denied(err.Error())
		}
	case admissionv1.Delete:
		// no validation needed on delete
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation request %q", req.Operation))
	}

	return admission.Allowed("")
}

func validateInfraClusterSecretRef(ref *corev1.ObjectReference, resourceNamespace, controllerNamespace string) error {
	if ref == nil {
		return nil
	}
	if ref.Namespace == "" || ref.Namespace == resourceNamespace {
		return nil
	}
	if controllerNamespace != "" && ref.Namespace == controllerNamespace {
		return nil
	}
	return fmt.Errorf(
		"spec.infraClusterSecretRef.namespace must be empty, match the resource namespace %q, or reference the controller namespace; got %q",
		resourceNamespace, ref.Namespace)
}
