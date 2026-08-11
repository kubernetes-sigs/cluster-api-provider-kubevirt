package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const cloudInitDNSServer = "1.1.1.1"

func cloudInitNetworkDataManifests(manifests []byte, namespace string, dataSource infrav1.CloudInitDataSource) ([]byte, []*corev1.Secret, map[string]string, error) {
	// Each template creates exactly one guest in this test.
	addresses := map[string]struct{ mac, ipv6 string }{
		"kvcluster-control-plane": {mac: "02:00:00:00:00:01", ipv6: "2001:db8::10"},
		"kvcluster-md-0":          {mac: "02:00:00:00:00:02", ipv6: "2001:db8::11"},
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	var documents []string
	var secrets []*corev1.Secret
	networkDataByMAC := map[string]string{}
	for {
		var document runtime.RawExtension
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, nil, fmt.Errorf("decode cluster manifest: %w", err)
		}
		if len(document.Raw) == 0 {
			continue
		}
		var typeMeta metav1.TypeMeta
		if err := json.Unmarshal(document.Raw, &typeMeta); err != nil {
			return nil, nil, nil, fmt.Errorf("decode manifest type: %w", err)
		}
		if typeMeta.Kind != "KubevirtMachineTemplate" {
			documents = append(documents, string(document.Raw))
			continue
		}
		var template infrav1.KubevirtMachineTemplate
		if err := json.Unmarshal(document.Raw, &template); err != nil {
			return nil, nil, nil, fmt.Errorf("decode machine template: %w", err)
		}
		address, ok := addresses[template.Name]
		if !ok {
			return nil, nil, nil, fmt.Errorf("unexpected machine template %q", template.Name)
		}
		delete(addresses, template.Name)
		spec := &template.Spec.Template.Spec
		if spec.VirtualMachineTemplate.Spec.Template == nil {
			return nil, nil, nil, fmt.Errorf("machine template %q has no VMI template", template.Name)
		}
		vmiSpec := &spec.VirtualMachineTemplate.Spec.Template.Spec
		vmiSpec.Domain.Devices.Interfaces = []kubevirtv1.Interface{{
			Name: "default", MacAddress: address.mac,
			InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{Bridge: &kubevirtv1.InterfaceBridge{}},
		}}
		vmiSpec.Networks = []kubevirtv1.Network{{
			Name: "default", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}},
		}}
		spec.CloudInit = &infrav1.CloudInitSpec{DataSource: dataSource}
		var networkData string
		switch dataSource {
		case infrav1.CloudInitDataSourceNoCloud:
			networkData = "version: 2\nethernets:\n  default:\n    match:\n      name: \"*\"\n    dhcp4: true\n    nameservers:\n      addresses: [" + cloudInitDNSServer + "]\n"
			spec.CloudInit.NetworkData = ptr.To(networkData)
		case infrav1.CloudInitDataSourceConfigDrive:
			// Ubuntu's v1-to-netplan renderer only emits DNS for static subnets.
			networkData = fmt.Sprintf(`{
  "links": [{"id": "default", "type": "phy", "ethernet_mac_address": %q}],
  "networks": [
    {"id": "default", "type": "ipv4_dhcp", "link": "default"},
    {"id": "dns", "type": "ipv6", "link": "default",
     "ip_address": %q, "netmask": "128",
     "services": [{"type": "dns", "address": %q}]}
  ],
  "services": []
}`, address.mac, address.ipv6, cloudInitDNSServer)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: template.Name + "-network-data", Namespace: namespace},
				Data:       map[string][]byte{"networkData": []byte(networkData)},
			}
			secrets = append(secrets, secret)
			spec.CloudInit.NetworkDataSecretRef = &corev1.LocalObjectReference{Name: secret.Name}
		default:
			return nil, nil, nil, fmt.Errorf("unsupported cloud-init data source %q", dataSource)
		}
		networkDataByMAC[address.mac] = networkData
		encoded, err := json.Marshal(&template)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("encode machine template %q: %w", template.Name, err)
		}
		documents = append(documents, string(encoded))
	}
	if len(addresses) != 0 {
		return nil, nil, nil, fmt.Errorf("missing machine templates: %v", addresses)
	}
	return []byte(strings.Join(documents, "\n---\n")), secrets, networkDataByMAC, nil
}

func TestCloudInitNetworkDataManifests(t *testing.T) {
	_, sourceFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source file")
	}
	templatePath := filepath.Join(filepath.Dir(sourceFile), "../templates/cluster-template.yaml")
	manifests, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	manifests = bytes.ReplaceAll(manifests, []byte("${CLUSTER_NAME}"), []byte("kvcluster"))
	manifests = bytes.ReplaceAll(manifests, []byte("${CONTROL_PLANE_MACHINE_COUNT}"), []byte("1"))
	manifests = bytes.ReplaceAll(manifests, []byte("${WORKER_MACHINE_COUNT}"), []byte("1"))
	for _, dataSource := range []infrav1.CloudInitDataSource{infrav1.CloudInitDataSourceNoCloud, infrav1.CloudInitDataSourceConfigDrive} {
		t.Run(string(dataSource), func(t *testing.T) {
			result, secrets, expected, err := cloudInitNetworkDataManifests(manifests, "test", dataSource)
			if err != nil {
				t.Fatal(err)
			}
			decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(result), 4096)
			seenMACs := map[string]bool{}
			seenIPs := map[string]bool{}
			seenSecrets := map[string]bool{}
			documentCount := 0
			for {
				var template infrav1.KubevirtMachineTemplate
				if err := decoder.Decode(&template); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				documentCount++
				if template.Kind != "KubevirtMachineTemplate" {
					continue
				}
				spec := template.Spec.Template.Spec
				vmiSpec := spec.VirtualMachineTemplate.Spec.Template.Spec
				if spec.BootstrapCheckSpec.CheckStrategy != "ssh" || len(vmiSpec.Domain.Devices.Disks) != 1 || len(vmiSpec.Volumes) != 1 || vmiSpec.Volumes[0].ContainerDisk == nil {
					t.Fatalf("original bootstrap or disk configuration was lost in %s", template.Name)
				}
				if len(vmiSpec.Networks) != 1 || vmiSpec.Networks[0].Pod == nil || len(vmiSpec.Domain.Devices.Interfaces) != 1 {
					t.Fatalf("expected one pod interface in %s", template.Name)
				}
				iface := vmiSpec.Domain.Devices.Interfaces[0]
				if iface.Bridge == nil || iface.Name != vmiSpec.Networks[0].Name || iface.MacAddress == "" || seenMACs[iface.MacAddress] {
					t.Fatalf("invalid or duplicate bridged interface: %+v", iface)
				}
				seenMACs[iface.MacAddress] = true
				if spec.CloudInit == nil || spec.CloudInit.DataSource != dataSource {
					t.Fatalf("unexpected cloud-init configuration in %s", template.Name)
				}
				if dataSource == infrav1.CloudInitDataSourceNoCloud {
					if spec.CloudInit.NetworkData == nil || *spec.CloudInit.NetworkData != expected[iface.MacAddress] || spec.CloudInit.NetworkDataSecretRef != nil {
						t.Fatalf("unexpected inline network data in %s", template.Name)
					}
					continue
				}
				ref := spec.CloudInit.NetworkDataSecretRef
				if ref == nil || seenSecrets[ref.Name] || spec.CloudInit.NetworkData != nil {
					t.Fatalf("missing or shared ConfigDrive source Secret in %s", template.Name)
				}
				seenSecrets[ref.Name] = true
				var matchedSecret *corev1.Secret
				for _, secret := range secrets {
					if secret.Name == ref.Name {
						matchedSecret = secret
					}
				}
				if matchedSecret == nil || matchedSecret.Namespace != "test" || string(matchedSecret.Data["networkData"]) != expected[iface.MacAddress] {
					t.Fatalf("incorrect source Secret for %s", template.Name)
				}
				var payload struct {
					Links []struct {
						MAC string `json:"ethernet_mac_address"`
					} `json:"links"`
					Networks []struct {
						IP       string `json:"ip_address"`
						Netmask  string `json:"netmask"`
						Services []struct {
							Type    string `json:"type"`
							Address string `json:"address"`
						} `json:"services"`
					} `json:"networks"`
				}
				if err := json.Unmarshal(matchedSecret.Data["networkData"], &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Links) != 1 || payload.Links[0].MAC != iface.MacAddress || len(payload.Networks) != 2 {
					t.Fatalf("ConfigDrive payload does not match %s", template.Name)
				}
				ip := payload.Networks[1].IP
				if ip == "" || seenIPs[ip] {
					t.Fatalf("missing or duplicate ConfigDrive static address: %q", ip)
				}
				subnet := payload.Networks[1]
				if subnet.Netmask != "128" || len(subnet.Services) != 1 || subnet.Services[0].Type != "dns" || subnet.Services[0].Address != cloudInitDNSServer {
					t.Fatalf("invalid ConfigDrive DNS subnet in %s: %+v", template.Name, subnet)
				}
				seenIPs[ip] = true
			}
			if documentCount != 7 || len(seenMACs) != 2 || len(expected) != 2 || len(secrets) != len(seenSecrets) {
				t.Fatalf("unexpected generated resources: documents=%d MACs=%v payloads=%d Secrets=%d", documentCount, seenMACs, len(expected), len(secrets))
			}
		})
	}
}
