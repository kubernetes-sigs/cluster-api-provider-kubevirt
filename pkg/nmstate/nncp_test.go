package nmstate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nmstate "github.com/nmstate/kubernetes-nmstate/api/shared"
)

var _ = Describe("NodeNetworkConfiguration functions", func() {

	It("should add IP to interface with special description", func() {
		obtainedInfraNodeNetworking, err := AddInfraNodeIP(nmstate.NewState(`
interfaces:
- name: eth0
  state: up 
  type: ethernet
- name: br-capk
  description: capk.cluster.x-k8s.io/interface
  state: up
  type: linux-bridge
  ipv4:
    enabled: true
    dhcp: false
`), "192.168.3.1", 24)
		Expect(err).ToNot(HaveOccurred())

		expectedInfraNodeNetworking := `interfaces:
- name: eth0
  state: up 
  type: ethernet
- name: br-capk
  description: capk.cluster.x-k8s.io/interface
  state: up
  type: linux-bridge
  ipv4:
    enabled: true
    dhcp: false
    address:
    - ip: 192.168.3.1
      prefix-length: 24
`
		Expect(expectedInfraNodeNetworking).To(MatchYAML(obtainedInfraNodeNetworking))
	})

	It("should find infra node IP from NNCP", func() {
		obtainedInfraNodeIP, err := FindInfraNodeIP(nmstate.NewState(`
interfaces:
- name: eth0
  state: up 
  type: ethernet
- name: br-capk
  description: capk.cluster.x-k8s.io/interface
  state: up
  type: linux-bridge
  ipv4:
    enabled: true
    dhcp: false
    address:
    - ip: 192.168.3.1
      prefix-length: 24

`))
		Expect(err).ToNot(HaveOccurred())
		Expect("192.168.3.1").To(Equal(obtainedInfraNodeIP))
	})
})
