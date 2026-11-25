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

package cloudinit

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const (
	networkDataJSON = `{"links":[{"id":"eth0","type":"phy","ethernet_mac_address":"52:54:00:00:00:01"}],"networks":[{"id":"net0","type":"ipv4_dhcp","link":"eth0"}]}`
	networkConfigV2 = "version: 2\nethernets:\n  eth0:\n    dhcp4: true\n"
	networkConfigV1 = "network:\n  version: 1\n  config:\n    - type: physical\n      name: eth0\n      subnets:\n        - type: dhcp\n"
)

func TestValidateNetworkData(t *testing.T) {
	tests := []struct {
		name       string
		dataSource infrav1.CloudInitDataSource
		data       string
		wantErr    string
	}{
		{name: "config drive: network_data.json", dataSource: infrav1.CloudInitDataSourceConfigDrive, data: networkDataJSON},
		{name: "unset data source is a config drive", dataSource: "", data: networkDataJSON},
		{name: "config drive: netplan style YAML", dataSource: infrav1.CloudInitDataSourceConfigDrive, data: networkConfigV2, wantErr: "must be a JSON object in the OpenStack network_data.json format"},
		{name: "unset data source: netplan style YAML", dataSource: "", data: networkConfigV2, wantErr: `for the "configDrive" data source`},
		{name: "config drive: JSON that is not an object", dataSource: infrav1.CloudInitDataSourceConfigDrive, data: `["links"]`, wantErr: "must be a JSON object"},
		{name: "config drive: truncated JSON", dataSource: infrav1.CloudInitDataSourceConfigDrive, data: `{"links": [`, wantErr: "must be a JSON object"},
		{name: "config drive: empty", dataSource: infrav1.CloudInitDataSourceConfigDrive, data: "", wantErr: "network data is empty"},
		{name: "NoCloud: network config version 2", dataSource: infrav1.CloudInitDataSourceNoCloud, data: networkConfigV2},
		{name: "NoCloud: network config version 1", dataSource: infrav1.CloudInitDataSourceNoCloud, data: networkConfigV1},
		{name: "NoCloud: JSON, which is YAML", dataSource: infrav1.CloudInitDataSourceNoCloud, data: `{"version": 2, "ethernets": {}}`},
		{name: "NoCloud: a sequence", dataSource: infrav1.CloudInitDataSourceNoCloud, data: "- not\n- a mapping\n", wantErr: "must be a YAML mapping in the cloud-init network configuration format"},
		{name: "NoCloud: a scalar", dataSource: infrav1.CloudInitDataSourceNoCloud, data: "version 2", wantErr: "must be a YAML mapping"},
		{name: "NoCloud: broken YAML", dataSource: infrav1.CloudInitDataSourceNoCloud, data: "version: 2\n  ethernets: [", wantErr: "must be a YAML mapping"},
		{name: "NoCloud: only a comment", dataSource: infrav1.CloudInitDataSourceNoCloud, data: "# nothing\n", wantErr: "must be a non-empty YAML mapping"},
		{name: "NoCloud: empty", dataSource: infrav1.CloudInitDataSourceNoCloud, data: "", wantErr: "network data is empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNetworkData(tt.dataSource, []byte(tt.data))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("expected no error, got: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got none", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("expected an error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    *infrav1.CloudInitSpec
		wantErr string
	}{
		{name: "no cloud-init specification"},
		{name: "no network data", spec: &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud}},
		{name: "well formed inline network data", spec: &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud, NetworkData: ptr.To(networkConfigV2)}},
		{
			name:    "inline network data in the format of the other data source",
			spec:    &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceConfigDrive, NetworkData: ptr.To(networkConfigV2)},
			wantErr: "invalid cloudInit.networkData: network data for the \"configDrive\" data source must be a JSON object",
		},
		{
			// What the secret holds is the controller's to check, when it reads it.
			name: "a referenced secret is not looked at",
			spec: &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceConfigDrive, NetworkDataSecretRef: &corev1.LocalObjectReference{Name: "network-data"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpec(tt.spec)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("expected no error, got: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got none", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("expected an error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestNetworkDataSecretName(t *testing.T) {
	if got := NetworkDataSecretName("kvcluster-md-0-abcde"); got != "kvcluster-md-0-abcde-networkdata" {
		t.Fatalf("unexpected network data secret name: %s", got)
	}
}
