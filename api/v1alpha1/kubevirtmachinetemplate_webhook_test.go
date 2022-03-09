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

package v1alpha1

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type test struct {
	name        string
	newTemplate *KubevirtMachineTemplate
	oldTemplate *KubevirtMachineTemplate
	wantError   bool
}

var _ = Describe("Template Validation", func() {
	var tests test
	Context("Template comparison without any errors", func() {
		BeforeEach(func() {
			tests = test{
				name: "return no error if no modification",
				oldTemplate: &KubevirtMachineTemplate{
					Spec: KubevirtMachineTemplateSpec{
						Template: KubevirtMachineTemplateResource{
							Spec: KubevirtMachineSpec{},
						},
					},
				},
				newTemplate: &KubevirtMachineTemplate{
					Spec: KubevirtMachineTemplateSpec{
						Template: KubevirtMachineTemplateResource{
							Spec: KubevirtMachineSpec{},
						},
					},
				},
				wantError: false,
			}
		})

		It("should not return error", func() {
			err := tests.newTemplate.ValidateUpdate(tests.oldTemplate)
			Ω(err).ShouldNot(HaveOccurred())
		})
	})
	Context("Template comparison with errors", func() {
		BeforeEach(func() {
			providerID := "test"
			tests = test{
				name: "return no error if no modification",
				oldTemplate: &KubevirtMachineTemplate{
					Spec: KubevirtMachineTemplateSpec{
						Template: KubevirtMachineTemplateResource{
							Spec: KubevirtMachineSpec{
								ProviderID: nil,
							},
						},
					},
				},
				newTemplate: &KubevirtMachineTemplate{
					Spec: KubevirtMachineTemplateSpec{
						Template: KubevirtMachineTemplateResource{
							Spec: KubevirtMachineSpec{
								ProviderID: &providerID,
							},
						},
					},
				},
				wantError: false,
			}
		})

		It("should return error", func() {
			err := tests.newTemplate.ValidateUpdate(tests.oldTemplate)
			Expect(err).Should(MatchError(errors.New("KubevirtMachineTemplateSpec is immutable")))
		})
	})
})
