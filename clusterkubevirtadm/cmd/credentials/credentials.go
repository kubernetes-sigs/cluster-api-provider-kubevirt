/*
Copyright 2021 The Kubernetes Authors.

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

package credentials

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

const (
	roleName        = "capk-user-role"
	roleBindingName = roleName + "-binding"
)

type cmdContext struct {
	Client    k8sclient.Interface
	Namespace string
}

type clientOperation string

const (
	clientOperationApply  clientOperation = "apply"
	clientOperationCreate clientOperation = "create"
)

func ensureNamespace(ctx context.Context, cmdCtx cmdContext, co clientOperation) error {
	_, err := cmdCtx.Client.CoreV1().Namespaces().Get(ctx, cmdCtx.Namespace, metav1.GetOptions{})
	if err == nil {
		if co == clientOperationApply {
			common.CmdLog("Found namespace", cmdCtx.Namespace)
			return nil
		}
		return fmt.Errorf("namspace %s is already exist", cmdCtx.Namespace)
	}

	if errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cmdCtx.Namespace,
			},
		}
		common.CmdLog("Creating namespace", cmdCtx.Namespace)
		_, err = cmdCtx.Client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create namespace %s; %w", cmdCtx.Namespace, err)
		}
		return nil
	}

	return fmt.Errorf("failed to read namespace %s ; %w", cmdCtx.Namespace, err)
}

func ensureServiceAccount(ctx context.Context, cmdCtx cmdContext) error {
	_, err := cmdCtx.Client.CoreV1().ServiceAccounts(cmdCtx.Namespace).Get(ctx, common.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			err = createServiceAccount(ctx, cmdCtx)
			if err != nil {
				return fmt.Errorf("failed to create the service account %s; %w", common.ServiceAccountName, err)
			}
			return nil
		} else {
			return fmt.Errorf("can't get the serviceAccount; %w", err)
		}
	}

	common.CmdLog("Found ServiceAccount", common.ServiceAccountName)
	return nil
}

func createServiceAccount(ctx context.Context, cmdCtx cmdContext) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.ServiceAccountName,
			Namespace: cmdCtx.Namespace,
		},
	}

	common.CmdLog("Creating ServiceAccount", common.ServiceAccountName)
	_, err := cmdCtx.Client.CoreV1().ServiceAccounts(cmdCtx.Namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func createOrUpdateRole(ctx context.Context, cmdCtx cmdContext, co clientOperation) error {
	requiredRole := generateRole(cmdCtx)
	existingRole, err := cmdCtx.Client.RbacV1().Roles(cmdCtx.Namespace).Get(ctx, requiredRole.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			common.CmdLog("creating Role", requiredRole.Name)
			_, err = cmdCtx.Client.RbacV1().Roles(cmdCtx.Namespace).Create(ctx, requiredRole, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("can't create existingRole; %w", err)
			}
			return nil
		} else {
			return fmt.Errorf("can't read existingRole %s; %w", roleName, err)
		}
	}

	if co == clientOperationApply {
		common.CmdLog("Found Role", roleName)
		if !reflect.DeepEqual(existingRole.Rules, requiredRole.Rules) {
			existingRole.Rules = requiredRole.Rules
			common.CmdLog("Updating Role", roleName)
			_, err = cmdCtx.Client.RbacV1().Roles(cmdCtx.Namespace).Update(ctx, existingRole, metav1.UpdateOptions{})
			return err
		}
		return nil
	}

	// should never get here: if the NS is already exist in create, we'll exit much earlier. On apply we'll get into the
	// previous condition
	return fmt.Errorf("role %s is already exist", roleName)
}

func generateRole(cmdCtx cmdContext) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: cmdCtx.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"kubevirt.io"},
				Resources: []string{"virtualmachines", "virtualmachineinstances"},
				Verbs:     []string{rbacv1.VerbAll},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets", "services"},
				Verbs:     []string{rbacv1.VerbAll},
			},
		},
	}
}

func ensureRoleBinding(ctx context.Context, cmdCtx cmdContext) error {
	rb := generateRoleBinding(cmdCtx)
	_, err := cmdCtx.Client.RbacV1().RoleBindings(cmdCtx.Namespace).Get(ctx, rb.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			common.CmdLog("creating RoleBinding", rb.Name)
			_, err = cmdCtx.Client.RbacV1().RoleBindings(cmdCtx.Namespace).Create(ctx, rb, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("can't create roleBinding; %w", err)
			} else {
				return nil
			}
		} else {
			return fmt.Errorf("can't read roleBinding %s; %w", rb.Name, err)
		}
	}

	common.CmdLog("Found RoleBinding", rb.Name)
	return nil
}

func generateRoleBinding(cmdCtx cmdContext) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: cmdCtx.Namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Namespace: cmdCtx.Namespace,
				Kind:      "ServiceAccount",
				Name:      common.ServiceAccountName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     roleName,
		},
	}
}

func createOrUpdateResources(ctx context.Context, cmdCtx cmdContext, co clientOperation) error {
	err := ensureNamespace(ctx, cmdCtx, co)
	if err != nil {
		return err
	}

	err = ensureServiceAccount(ctx, cmdCtx)
	if err != nil {
		return err
	}

	err = createOrUpdateRole(ctx, cmdCtx, co)
	if err != nil {
		return err
	}

	err = ensureRoleBinding(ctx, cmdCtx)
	if err != nil {
		return err
	}

	return nil
}
