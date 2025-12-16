package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type tenantClusterAccess struct {
	listener             net.Listener
	namespace            string
	tenantKubeconfigFile string
	isForwarding         bool
	cancelFunc           func() error
	tenantApiPort        int
}

func newTenantClusterAccess(namespace string, tenantKubeconfigFile string) tenantClusterAccess {
	return tenantClusterAccess{
		namespace:            namespace,
		tenantKubeconfigFile: tenantKubeconfigFile,
	}
}

func (t *tenantClusterAccess) generateClient() (*kubernetes.Clientset, error) {
	localPort := t.getLocalPort()
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

func (t *tenantClusterAccess) startForwardingTenantAPI(ctx context.Context, cli client.Client) error {
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

	vmiName, err := t.findControlPlaneVMIName(ctx, cli)
	if err != nil {
		return err
	}

	if err = t.startPortForwarding(ctx, vmiName); err != nil {
		return err
	}

	t.isForwarding = true
	go t.waitForConnection()

	return nil
}

func (t *tenantClusterAccess) startPortForwarding(ctx context.Context, vmiName string) error {
	apiPort, err := getFreePort()
	if err != nil {
		return err
	}
	t.tenantApiPort = apiPort

	fmt.Printf("Found free port: %d\n", t.tenantApiPort)
	cmd := exec.CommandContext(ctx, VirtctlPath, "port-forward", "-n", t.namespace, fmt.Sprintf("vmi/%s", vmiName), fmt.Sprintf("%d:6443", t.tenantApiPort))
	t.cancelFunc = cmd.Cancel
	if t.cancelFunc == nil {
		klog.Errorln("unable to get cancel function for port-forward command")
		return nil
	}

	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if exitErr.String() == "signal: killed" {
					return
				}
			}
			klog.Errorf("error port-forwarding vmi %s/%s: %v, [%s]\n", t.namespace, vmiName, err, string(out))
		}
	}()

	return nil
}

func (t *tenantClusterAccess) stopPortForwarding() {
	if t.cancelFunc == nil {
		klog.Info("port forwarding already stopped")
		return
	}

	if err := t.cancelFunc(); err != nil {
		klog.Errorf("failed to stop port forwarding; %v", err)
	}
}

func (t *tenantClusterAccess) findControlPlaneVMIName(ctx context.Context, cli client.Client) (string, error) {
	vmiList := &kubevirtv1.VirtualMachineInstanceList{}
	err := cli.List(ctx, vmiList)
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
		return "", fmt.Errorf("couldn't find controlplane vmi in namespace %s", t.namespace)
	}
	return chosenVMI.Name, nil
}

func (t *tenantClusterAccess) stopForwardingTenantAPI() error {
	defer t.stopPortForwarding()
	if !t.isForwarding {
		return nil
	}
	t.isForwarding = false
	return t.listener.Close()
}

func (t *tenantClusterAccess) waitForConnection() {
	conn, err := t.listener.Accept()
	if err != nil {
		klog.Errorln("error accepting connection:", err)
		return
	}

	proxy, err := net.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(t.tenantApiPort)))
	if err != nil {
		klog.Errorf("unable to connect to local port-forward: %v", err)
		return
	}
	go t.handleConnection(conn, proxy)
}

// handleConnection copies data between the local connection and the stream to
// the remote server.
func (t *tenantClusterAccess) handleConnection(local, remote net.Conn) {
	defer func(local net.Conn) {
		err := local.Close()
		if err != nil {
			klog.Errorf("error closing local connection: %v", err)
		}
	}(local)
	defer func(remote net.Conn) {
		err := remote.Close()
		if err != nil {
			klog.Errorf("error closing remote connection: %v", err)
		}
	}(remote)
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
		klog.Errorf("error handling portForward connection: %v", err)
	}
}

func getFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer func(l *net.TCPListener) {
				err := l.Close()
				if err != nil {
					klog.Errorf("error closing tcp listener: %v", err)
				}
			}(l)
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}
