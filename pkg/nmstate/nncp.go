package nmstate

import (
	"fmt"

	nmstate "github.com/nmstate/kubernetes-nmstate/api/shared"

	"gopkg.in/yaml.v3"
)

func GenerateNNCPName(clusterName, nodeName string) string {
	return clusterName + "-" + nodeName
}

func AddInfraNodeIP(nodeNetworkTemplate nmstate.State, nodeIP string, nodePrefix int) (*nmstate.State, error) {
	state := map[string]interface{}{}
	if err := yaml.Unmarshal(nodeNetworkTemplate.Raw, &state); err != nil {
		return nil, err
	}
	interfaces, err := readInterfaces(state)
	if err != nil {
		return nil, err
	}
	ifaceIdx, iface, err := findTenantClusterInterface(interfaces)
	if err != nil {
		return nil, err
	}
	if iface == nil {
		return nil, fmt.Errorf("missing an interface with proper description")
	}

	interfaces[ifaceIdx], err = addAddressToInterface(iface, nodeIP, nodePrefix)
	if err != nil {
		return nil, err
	}
	state["interfaces"] = interfaces

	finalNetworking, err := yaml.Marshal(state)
	if err != nil {
		return nil, err
	}
	return &nmstate.State{Raw: finalNetworking}, nil
}

func FindInfraNodeIP(nodeNetworkState nmstate.State) (string, error) {
	state := map[string]interface{}{}
	if err := yaml.Unmarshal(nodeNetworkState.Raw, &state); err != nil {
		return "", err
	}
	interfaces, err := readInterfaces(state)
	if err != nil {
		return "", err
	}
	_, iface, err := findTenantClusterInterface(interfaces)
	if err != nil {
		return "", err
	}
	if iface == nil {
		return "", fmt.Errorf("missing an interface with proper description")
	}
	ipv4, err := readIPv4(iface)
	if err != nil {
		return "", err
	}
	addresses, err := readAddresses(ipv4)
	if err != nil {
		return "", err
	}
	if addresses == nil || len(addresses) == 0 {
		return "", nil
	}
	return addresses[0]["ip"].(string), nil

}

func readInterfaces(state map[string]interface{}) ([]interface{}, error) {
	interfacesRaw, ok := state["interfaces"]
	if !ok {
		return nil, fmt.Errorf("missing interfaces")
	}

	interfaces, ok := interfacesRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected interfaces type")
	}
	return interfaces, nil
}

func findTenantClusterInterface(interfaces []interface{}) (int, map[string]interface{}, error) {
	for i, iface := range interfaces {
		ifaceMap, ok := iface.(map[string]interface{})
		if !ok {
			return 0, nil, fmt.Errorf("unexpected interface type")
		}
		description, hasDescription := ifaceMap["description"]
		if hasDescription && description == "capk.cluster.x-k8s.io/interface" {
			return i, ifaceMap, nil
		}
	}
	return 0, nil, nil
}

func addAddressToInterface(iface map[string]interface{}, address string, prefix int) (map[string]interface{}, error) {
	ipv4, err := readIPv4(iface)
	if err != nil {
		return nil, err
	}

	addresses, err := readAddresses(ipv4)
	if err != nil {
		return nil, err
	}
	addresses = append(addresses, map[string]interface{}{"ip": address, "prefix-length": prefix})
	ipv4["address"] = addresses
	iface["ipv4"] = ipv4
	return iface, nil
}

func readAddresses(iface map[string]interface{}) ([]map[string]interface{}, error) {
	addressRaw, found := iface["address"]
	if !found {
		return []map[string]interface{}{}, nil
	}

	addressList, ok := addressRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected address format")
	}
	addresses := []map[string]interface{}{}
	for _, addressRaw := range addressList {
		address, ok := addressRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected address entry format")
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func readIPv4(iface map[string]interface{}) (map[string]interface{}, error) {
	ipv4Raw, found := iface["ipv4"]
	if !found {
		return map[string]interface{}{}, nil
	}

	ipv4, ok := ipv4Raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected ipv4 type")
	}
	return ipv4, nil
}
