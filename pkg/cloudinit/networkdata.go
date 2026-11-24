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

// Package cloudinit holds what the webhook and the machine controller share about the cloud-init
// network data of a KubevirtMachine.
package cloudinit

import (
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

// NetworkDataKey is the key CAPK writes the network data under, in the secret it creates in the
// infra cluster.
const NetworkDataKey = "networkdata"

// NetworkDataSecretKeys are the keys looked up, in order, in a referenced network data secret.
// They mirror the keys KubeVirt itself reads from a network data secret, see
// readFirstFoundFileFromDir in kubevirt/kubevirt pkg/cloud-init/cloud-init.go.
var NetworkDataSecretKeys = []string{NetworkDataKey, "networkData"}

// NetworkDataSecretName returns the name of the secret holding the network data of a machine in
// the infra cluster. The secret is specific to the machine: machines can share a bootstrap data
// secret, and so its "-userdata" copy, while each of them needs its own network data.
func NetworkDataSecretName(kubevirtMachineName string) string {
	return kubevirtMachineName + "-networkdata"
}

// ValidateNetworkData checks that a network data document is well formed for the given data source:
// a JSON object for a config drive, which cloud-init reads as OpenStack network_data.json, and a
// YAML mapping for NoCloud, which cloud-init reads as its network configuration format.
//
// The check is deliberately shallow. It does not validate the document against the schema of
// either format; it catches the documents no guest could ever apply, which would otherwise surface
// only as a bootstrap timeout.
func ValidateNetworkData(dataSource infrav1.CloudInitDataSource, data []byte) error {
	if len(data) == 0 {
		return errors.New("network data is empty")
	}

	switch dataSource {
	case infrav1.CloudInitDataSourceNoCloud:
		document := map[string]interface{}{}
		if err := yaml.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("network data for the %q data source must be a YAML mapping in the cloud-init network configuration format: %w", dataSource, err)
		}
		if len(document) == 0 {
			return fmt.Errorf("network data for the %q data source must be a non-empty YAML mapping in the cloud-init network configuration format", dataSource)
		}
	default:
		document := map[string]interface{}{}
		if err := json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("network data for the %q data source must be a JSON object in the OpenStack network_data.json format: %w", infrav1.CloudInitDataSourceConfigDrive, err)
		}
	}

	return nil
}

// ValidateSpec checks the inline network data of a cloud-init specification, if there is any.
// Network data held in a referenced secret is checked by the machine controller when it reads it.
func ValidateSpec(spec *infrav1.CloudInitSpec) error {
	data := spec.GetNetworkData()
	if data == nil {
		return nil
	}

	if err := ValidateNetworkData(spec.GetDataSource(), []byte(*data)); err != nil {
		return fmt.Errorf("invalid cloudInit.networkData: %w", err)
	}

	return nil
}
