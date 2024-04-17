/*
Copyright 2023 The Kubernetes Authors.

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
	"fmt"
	"net/http"
	"reflect"

	admissionv1 "k8s.io/api/admission/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const (
	webhookValidationPath = "/validate-infrastructure-cluster-x-k8s-io-v1alpha1-kubevirtmachinetemplate"
	immutableWarning      = "KubevirtMachineTemplateSpec is immutable"
)

func SetupWebhookWithManager(mgr ctrl.Manager) error {
	decoder := admission.NewDecoder(mgr.GetScheme())

	whHandler := &kubevirtMachineTemplateHandler{
		decoder: decoder,
	}

	srv := mgr.GetWebhookServer()

	srv.Register(webhookValidationPath, &webhook.Admission{Handler: whHandler})

	return nil
}

type kubevirtMachineTemplateHandler struct {
	decoder *admission.Decoder
}

func (wh *kubevirtMachineTemplateHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	// Get the object in the request
	kvTmplt := &v1alpha1.KubevirtMachineTemplate{}

	var err error
	switch req.Operation {
	case admissionv1.Create:
		if err := wh.decoder.Decode(req, kvTmplt); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

	case admissionv1.Update:
		oldKVTmplt := &v1alpha1.KubevirtMachineTemplate{}
		// Server Side Apply implementation in ClusterClass and managed topologies requires to dry-run changes on templates.
		// cf: https://cluster-api.sigs.k8s.io/developer/providers/migrations/v1.1-to-v1.2?search=#required-api-changes-for-providers	
		if *req.DryRun {
			return admission.Allowed("")
		}
		if err := wh.decoder.DecodeRaw(req.Object, kvTmplt); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if err := wh.decoder.DecodeRaw(req.OldObject, oldKVTmplt); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		err = wh.validateUpdate(oldKVTmplt, kvTmplt)
	case admissionv1.Delete:
		// In reference to PR: https://github.com/kubernetes/kubernetes/pull/76346
		// OldObject contains the object being deleted
		if err := wh.decoder.DecodeRaw(req.OldObject, kvTmplt); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation request %q", req.Operation))
	}

	// Check the error message first.
	if err != nil {
		return admission.Denied(err.Error())
	}

	// Return allowed if everything succeeded.
	return admission.Allowed("")
}

func (wh *kubevirtMachineTemplateHandler) validateUpdate(old *v1alpha1.KubevirtMachineTemplate, requested *v1alpha1.KubevirtMachineTemplate) error {
	if !reflect.DeepEqual(old.Spec, requested.Spec) {
		return errors.New(immutableWarning)
	}

	return nil
}
