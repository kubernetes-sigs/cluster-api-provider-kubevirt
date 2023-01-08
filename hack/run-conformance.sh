#!/bin/bash

set -e -o pipefail

echo "Running sonobuoy conformance e2e test suite"
export KUBECONFIG=$(./kubevirtci kubeconfig)
export TENANT_CLUSTER_NAME=${TENANT_CLUSTER_NAME:-kvcluster}
export TENANT_CLUSTER_NAMESPACE=${TENANT_CLUSTER_NAMESPACE:-kvcluster}

id=$(echo $RANDOM | sha1sum | head -c 4)
sonobuoy_release="https://github.com/vmware-tanzu/sonobuoy/releases/download/v0.56.8/sonobuoy_0.56.8_linux_amd64.tar.gz"
sonobuoy_result_tarball="results.tar.gz"
sonobuoy_plugin="e2e"
sonobuoy_pod_runner="${TENANT_CLUSTER_NAME}-sonobuoy-runner-$id"
sonobuoy_confimap="${TENANT_CLUSTER_NAME}-sonobuoy-config-$id"
sonobuoy_conformance_dir="/tmp/sonobuoy_conformance"
sonobuoy_status="sonobuoy_status.json"

function teardown() {
  ./kubevirtci kubectl delete pod --wait=false ${sonobuoy_pod_runner} -n ${TENANT_CLUSTER_NAMESPACE} > /dev/null 2>&1
  ./kubevirtci kubectl delete cm --wait=false ${sonobuoy_confimap} -n ${TENANT_CLUSTER_NAMESPACE}
  cp $sonobuoy_conformance_dir/$sonobuoy_result_tarball $ARTIFACTS
}

cat <<EOF | ./kubevirtci kubectl create -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${sonobuoy_confimap}
  namespace: ${TENANT_CLUSTER_NAMESPACE}
data:
  skip: |
    patching/updating a validating webhook should work
    should mutate pod and apply defaults after mutation
    should serve a basic image on each replica with a public image
    should create and stop a working application
    should be able to change the type from NodePort to ExternalName
    should provide /etc/hosts entries for the cluster
    should be able to change the type from ExternalName to ClusterIP
    should mutate custom resource
    listing mutating webhooks should work
    should not be able to mutate or prevent deletion of webhook configuration objects
    should be able to deny custom resource creation, update and deletion
    should honor timeout
    should mutate configmap
    should be able to convert a non homogeneous list of CRs
    should be able to convert from CR v1 to CR v2
    listing validating webhooks should work
    should unconditionally reject operations on fail closed webhook
    should deny crd creation
    should be able to deny pod and configmap creation
    should mutate custom resource with different stored version
    patching/updating a mutating webhook should work
    should scale a replication controller
    should be able to deny attaching pod
    should mutate custom resource with pruning
    should proxy through a service and a pod
    should support OIDC discovery of service account issuer
    should create and stop a replication controller
    should provide DNS for pods for Subdomain
    should be able to change the type from ClusterIP to ExternalName
    should provide DNS for the cluster
    should be able to switch session affinity for NodePort service
    should resolve DNS of partial qualified names for services
    Should be able to support the 1.17 Sample API Server using the current Aggregator
    should be able to create a functioning NodePort service
    should have session affinity work for NodePort service
    should provide DNS for pods for Hostname
    should rollback without unnecessary restarts
    should have session affinity timeout work for NodePort service
    should call prestop when killing a pod
    should serve a basic endpoint from pods
    should provide DNS for ExternalName services
    should be able to switch session affinity for service with type clusterIP
    should serve multiport endpoints from pods
    should have session affinity work for service with type clusterIP
    should serve a basic image on each replica with a public image
    should be able to change the type from ExternalName to NodePort
    should provide DNS for services
    should have session affinity timeout work for service with type clusterIP
EOF

cat <<EOF | ./kubevirtci kubectl create -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${sonobuoy_pod_runner}
  namespace: ${TENANT_CLUSTER_NAMESPACE}
spec:
  containers:
  - name: sonobuoy
    image: registry.access.redhat.com/ubi8/ubi:8.0
    env:
    - name: KUBECONFIG
      value: /etc/kubernetes/kubeconfig/value
    - name: SONOBUOY_CONFIG_PATH
      value: "/etc/config"
    - name: SONOBUOY_PLUGIN
      value: $sonobuoy_plugin
    command:
    - /bin/bash
    - -c
    - |
      dnf install -y jq
      curl -L $sonobuoy_release -o sonobuoy.tar.gz && tar -xzf sonobuoy.tar.gz
      chmod +x sonobuoy
      SKIP_TESTS=\${SKIP_TESTS:-""}
      if [[ -f "\$SONOBUOY_CONFIG_PATH/skip" ]]; then
        while read line; do
          SKIP_TESTS+="\$line|"
        done < "\$SONOBUOY_CONFIG_PATH/skip"
      fi
      SKIP_TESTS=\${SKIP_TESTS::-1}

      ./sonobuoy run --plugin \$SONOBUOY_PLUGIN --wait --e2e-skip="\$SKIP_TESTS"
      ./sonobuoy retrieve -f $sonobuoy_result_tarball
      ./sonobuoy status --json > $sonobuoy_status
      sleep 3600
    lifecycle:
      preStop:
        exec:
          command: ["/bin/bash", "-c", "./sonobuoy delete"]
    volumeMounts:
    - name: kubeconfig
      mountPath: "/etc/kubernetes/kubeconfig"
      readOnly: true
    - name: sonobuoy-config
      mountPath: "/etc/config"
      readOnly: true
  volumes:
  - name: kubeconfig
    secret:
      secretName: ${TENANT_CLUSTER_NAME}-kubeconfig
  - name: sonobuoy-config
    configMap:
      name: ${sonobuoy_confimap}
EOF
trap teardown EXIT SIGSTOP SIGKILL SIGTERM

sleep 10
while [[ ! $(./kubevirtci kubectl exec -n $TENANT_CLUSTER_NAMESPACE ${sonobuoy_pod_runner} -- ls $sonobuoy_status 2>/dev/null) ]]; do
  sleep 30
done

rm -rf $sonobuoy_conformance_dir
mkdir -p $sonobuoy_conformance_dir
for file in $sonobuoy_status $sonobuoy_result_tarball ; do
  ./kubevirtci kubectl cp ${TENANT_CLUSTER_NAMESPACE}/${sonobuoy_pod_runner}:/$file $sonobuoy_conformance_dir/$file
done
(
  cd $sonobuoy_conformance_dir
  tar -xvf $sonobuoy_result_tarball >/dev/null
  cat plugins/${sonobuoy_plugin}/results/global/${sonobuoy_plugin}.log
)

passed=$(cat $sonobuoy_conformance_dir/$sonobuoy_status | jq  ' .plugins[] | select(."result-status" == "passed")'  | wc -l)
failed=$(cat $sonobuoy_conformance_dir/$sonobuoy_status | jq  ' .plugins[] | select(."result-status" == "failed")'  | wc -l)

if [ "$passed" -eq 0 ] || [ "$failed" -ne 0 ]; then
  echo "sonobuoy failed running conformance tests for plugin $sonobuoy_plugin"
  exit 1
fi
