#!/bin/bash -xe
tenant_kubeconfig=tenant-kubeconfig
infra_kubeconfig=/root/quique/ocp-dev-cluster/dev-scripts/ocp/hypershift/auth/kubeconfig

export SERVER_NODE=cluster1-jq2wp
#export SERVER_NODE=worker-1
#export CLIENT_NODE=cluster1-9xdmv
export CLIENT_NODE=worker-1

function cleanup() {
    oc delete pod tcprobe-server tcprobe-client --ignore-not-found
    oc delete svc tcprobe-server --ignore-not-found
}

export KUBECONFIG=$tenant_kubeconfig
cleanup
export KUBECONFIG=$infra_kubeconfig
cleanup

export SERVER_NODE_IP=$(oc get vmi -n clusters-cluster1 $SERVER_NODE --no-headers |awk '{print $4}')

function server_ip() {
    oc get pod -o wide --no-headers |awk '{print $6}'
}

if [[ "$SERVER_NODE" =~ "worker" ]]; then
        export KUBECONFIG=$infra_kubeconfig
else
        export KUBECONFIG=$tenant_kubeconfig
fi

cat tcprobe-server.yaml |envsubst | oc apply -f -
oc apply -f service.yaml
while [[ "$(server_ip)" == "<none>" ]]; do
    sleep 1
done

export CLUSTER_IP=$(oc get svc tcprobe-server --no-headers |awk '{print $3}')
export NODE_PORT=$(oc get svc tcprobe-server -o json |jq .spec.ports[0].nodePort)
export SERVER_IP=$(server_ip)

#export REMOTE_PORT=4444
export REMOTE_PORT=${NODE_PORT}
export REMOTE_IP=${SERVER_NODE_IP}

if [[ "$CLIENT_NODE" =~ "worker" ]]; then
        export KUBECONFIG=$infra_kubeconfig
else
        export KUBECONFIG=$tenant_kubeconfig
fi
cat tcprobe-client.yaml |envsubst | oc apply -f -
