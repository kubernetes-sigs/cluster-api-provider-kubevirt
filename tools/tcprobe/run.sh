#!/bin/bash -xe
config=tcprobe.yaml
client_kubeconfig=$(cat $config | yq -r .client.kubeconfig)
server_kubeconfig=$(cat $config | yq -r .server.kubeconfig)
export CLIENT_NODE=$(cat $config | yq -r .client.node)
export SERVER_NODE=$(cat $config | yq -r .server.node)

function oc-s() {
    oc --kubeconfig $server_kubeconfig $@
}

function oc-c() {
    oc --kubeconfig $client_kubeconfig $@
}


function cleanup() {
    oc --kubeconfig $1 delete pod tcprobe-server tcprobe-client --ignore-not-found
    oc --kubeconfig $1 delete svc tcprobe-server --ignore-not-found
}

cleanup $server_kubeconfig
cleanup $client_kubeconfig

export SERVER_NODE_IP=$(oc-s get node $SERVER_NODE -o json |jq -r .status.addresses[0].address)

function server_ip() {
    oc-s get svc tcprobe-server -o json |jq -r .status.loadBalancer.ingress[0].ip
}

cat tcprobe-server.yaml |envsubst | oc-s apply -f -
oc-s apply -f service.yaml
while [[ "$(server_ip)" == "null" ]]; do
    sleep 1
done
export SERVER_IP=$(server_ip)

export REMOTE_PORT=4444
export REMOTE_IP=$SERVER_IP
cat tcprobe-client.yaml |envsubst | oc-c apply -f -
oc-c wait pod tcprobe-client --for=condition=Ready --timeout=10s
