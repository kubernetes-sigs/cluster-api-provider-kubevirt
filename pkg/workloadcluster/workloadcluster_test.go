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

package workloadcluster

import (
	gocontext "context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var _ = Describe("a workload cluster apiserver that never answers", func() {
	var (
		workload       WorkloadCluster
		unanswered     chan struct{}
		server         *httptest.Server
		machineContext *context.MachineContext
	)

	BeforeEach(func() {
		unanswered = make(chan struct{})
		server = httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-unanswered
		}))

		kubevirtCluster := testing.NewKubevirtCluster("test-cluster", "test-kubevirt-cluster")
		cluster := testing.NewCluster("test-cluster", kubevirtCluster)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: workload
  user:
    token: test
contexts:
- name: workload
  context:
    cluster: workload
    user: workload
current-context: workload
`, server.URL, base64.StdEncoding.EncodeToString(ca))
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + "-kubeconfig", Namespace: kubevirtCluster.Namespace},
			Data:       map[string][]byte{"value": []byte(kubeconfig)},
		}
		machineContext = &context.MachineContext{
			Context:         gocontext.Background(),
			Cluster:         cluster,
			KubevirtCluster: kubevirtCluster,
		}
		workload = New(fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(secret).Build(), 500*time.Millisecond)
	})

	AfterEach(func() {
		close(unanswered)
		server.Close()
	})

	It("fails a controller-runtime client request instead of blocking the reconcile", func() {
		c, err := workload.GenerateWorkloadClusterClient(machineContext)
		Expect(err).ToNot(HaveOccurred())

		done := make(chan error, 1)
		go func() { done <- c.Get(gocontext.Background(), client.ObjectKey{Name: "node"}, &corev1.Node{}) }()
		Eventually(done, 5*time.Second).Should(Receive(HaveOccurred()))
	})

	It("fails a clientset request instead of blocking the reconcile", func() {
		cs, err := workload.GenerateWorkloadClusterK8sClient(machineContext)
		Expect(err).ToNot(HaveOccurred())

		done := make(chan error, 1)
		go func() {
			_, err := cs.CoreV1().Nodes().Get(gocontext.Background(), "node", metav1.GetOptions{})
			done <- err
		}()
		Eventually(done, 5*time.Second).Should(Receive(HaveOccurred()))
	})
})
