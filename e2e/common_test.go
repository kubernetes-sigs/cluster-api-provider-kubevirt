package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/golang/glog"
	. "github.com/onsi/gomega"
	kubevirtv1 "kubevirt.io/api/core/v1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeleteAndWait function deletes a kubernetes object, with a timeout
func DeleteAndWait(k8sclient client.Client, obj client.Object, timeoutSeconds uint) {
	key := client.ObjectKeyFromObject(obj)
	Eventually(func() error {
		err := k8sclient.Get(context.Background(), key, obj)
		if err != nil && k8serrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}

		if obj.GetDeletionTimestamp().IsZero() {
			err = k8sclient.Delete(context.Background(), obj)
			if err != nil {
				return err
			}
		}
		return fmt.Errorf("waiting on object %s to be deleted", key)
	}, time.Duration(timeoutSeconds)*time.Second, 1*time.Second).Should(BeNil())
}

// RunCmd function executes a command, and returns STDOUT and STDERR bytes
func RunCmd(cmd *exec.Cmd) (stdoutBytes []byte, stderrBytes []byte) {
	// creates to bytes.Buffer, these are both io.Writer and io.Reader
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	// create the command and assign the outputs
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// run the command
	ExpectWithOffset(1, cmd.Run()).To(Succeed(), fmt.Sprintf("failed to run %s, with arguments: %v; error response: %s", cmd.Path, cmd.Args, stderr.Bytes()))

	return stdout.Bytes(), stderr.Bytes()
}

type tenantClusterAccess struct {
	listener             net.Listener
	namespace            string
	tenantKubeconfigFile string
	isForwarding         bool
}

func newTenantClusterAccess(namespace string, tenantKubeconfigFile string) tenantClusterAccess {
	return tenantClusterAccess{
		namespace:            namespace,
		tenantKubeconfigFile: tenantKubeconfigFile,
	}
}

func (t *tenantClusterAccess) generateClient() (*kubernetes.Clientset, error) {
	localPort := t.listener.Addr().(*net.TCPAddr).Port
	cmd := exec.Command(ClusterctlPath, "get", "kubeconfig", "kvcluster",
		"--namespace", t.namespace)
	stdout, _ := RunCmd(cmd)
	if err := os.WriteFile(t.tenantKubeconfigFile, stdout, 0644); err != nil {
		return nil, err
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: t.tenantKubeconfigFile},
		&clientcmd.ConfigOverrides{
			ClusterInfo: clientcmdapi.Cluster{
				Server:                fmt.Sprintf("https://127.0.0.1:%d", localPort),
				InsecureSkipTLSVerify: true,
			},
		})
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(restConfig)
}

func (t *tenantClusterAccess) getLocalPort() int {
	return t.listener.Addr().(*net.TCPAddr).Port
}

func (t *tenantClusterAccess) startForwardingTenantAPI() error {
	if t.isForwarding {
		return nil
	}
	address, err := net.ResolveIPAddr("", "127.0.0.1")
	if err != nil {
		return err
	}
	t.listener, err = net.ListenTCP(
		"tcp",
		&net.TCPAddr{
			IP:   address.IP,
			Zone: address.Zone,
		})
	if err != nil {
		return err
	}

	vmiName, err := t.findControlPlaneVMIName()
	if err != nil {
		return err
	}

	t.isForwarding = true
	go t.waitForConnection(vmiName, t.namespace)

	return nil
}

func (t *tenantClusterAccess) findControlPlaneVMIName() (string, error) {
	vmiList, err := virtClient.VirtualMachineInstance(t.namespace).List(&metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	var chosenVMI *kubevirtv1.VirtualMachineInstance
	for _, vmi := range vmiList.Items {
		if strings.Contains(vmi.Name, "-control-plane") {
			chosenVMI = &vmi
			break
		}
	}
	if chosenVMI == nil {
		return "", fmt.Errorf("Couldn't find controlplane vmi in namespace %s", t.namespace)
	}
	return chosenVMI.Name, nil
}

func (t *tenantClusterAccess) stopForwardingTenantAPI() error {
	if !t.isForwarding {
		return nil
	}
	t.isForwarding = false
	return t.listener.Close()
}

func (t *tenantClusterAccess) waitForConnection(name, namespace string) {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			glog.Errorln("error accepting connection:", err)
			return
		}
		stream, err := virtClient.VirtualMachineInstance(namespace).PortForward(name, 6443, "tcp")
		if err != nil {
			glog.Errorf("can't access vmi %s/%s: %v", namespace, name, err)
			return
		}
		go t.handleConnection(conn, stream.AsConn())
	}
}

// handleConnection copies data between the local connection and the stream to
// the remote server.
func (t *tenantClusterAccess) handleConnection(local, remote net.Conn) {
	defer local.Close()
	defer remote.Close()
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(remote, local)
		errs <- err
	}()
	go func() {
		_, err := io.Copy(local, remote)
		errs <- err
	}()

	t.handleConnectionError(<-errs)
}

func (t *tenantClusterAccess) handleConnectionError(err error) {
	if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
		glog.Errorf("error handling portForward connection: %v", err)
	}
}
