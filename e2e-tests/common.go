package e2e_tests

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	. "github.com/onsi/gomega"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func RunCmd(cmd *exec.Cmd) (stdoutBytes []byte, stderrBytes []byte) {
	stderr, err := cmd.StderrPipe()
	Expect(err).To(BeNil())

	stdout, err := cmd.StdoutPipe()
	Expect(err).To(BeNil())

	err = cmd.Start()
	Expect(err).To(BeNil())

	stdoutBytes, _ = io.ReadAll(stdout)
	stderrBytes, _ = io.ReadAll(stderr)
	if len(stderrBytes) > 0 {
		fmt.Printf("%s STDERR: %s\n", cmd.Path, stderrBytes)
	}

	err = cmd.Wait()
	Expect(err).To(BeNil())

	return stdoutBytes, stderrBytes
}
