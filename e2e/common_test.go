package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/gomega"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeleteAndWait function deletes a kubernetes object, with a timeout
func DeleteAndWait(ctx context.Context, k8sclient client.Client, obj client.Object, timeoutSeconds uint) {
	key := client.ObjectKeyFromObject(obj)
	Eventually(func() error {
		err := k8sclient.Get(ctx, key, obj)
		if err != nil && k8serrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}

		if obj.GetDeletionTimestamp().IsZero() {
			err = k8sclient.Delete(ctx, obj)
			if err != nil {
				return err
			}
		}
		return fmt.Errorf("waiting on object %s to be deleted", key)
	}, time.Duration(timeoutSeconds)*time.Second, 1*time.Second).Should(Succeed())
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
