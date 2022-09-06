#!/bin/bash -xe 
if [ ! -d ovn-kubernetes ]; then 
    git clone https://github.com/qinqon/ovn-kubernetes -b dnm-kind-and-multi-network
fi
pushd ovn-kubernetes
    pushd go-controller
    make
    popd

    pushd dist/images
    make fedora
    popd

    pushd contrib
    export KUBECONFIG=${HOME}/ovn.conf
    ./kind.sh
    popd
popd 
export KUBEVIRT_PROVIDER=external
for i in ovn-control-plane ovn-worker ovn-worker2; do 
    docker exec -t $i bash -c "echo 'fs.inotify.max_user_watches=1048576' >> /etc/sysctl.conf"
    docker exec -t $i bash -c "echo 'fs.inotify.max_user_instances=512' >> /etc/sysctl.conf"
    docker exec -i $i bash -c "sysctl -p /etc/sysctl.conf"
done
./kubevirtci down
./kubevirtci up
./kubevirtci install-capk
./kubevirtci create-cluster
./kubevirtci install-calico
