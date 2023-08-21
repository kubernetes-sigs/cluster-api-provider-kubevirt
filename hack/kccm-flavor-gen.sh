#!/bin/bash

set -x -e -o pipefail

kccm_template=/tmp/kccm.yaml

kubectl kustomize config/kccm > $kccm_template


sed -i \
    -E "s|(namespace: )kvcluster|\1\${NAMESPACE}|g;s|(^.*cluster-name=)kvcluster|\1\${CLUSTER_NAME}|g" \
    ${kccm_template}

for cluster_template in $(find templates/ -type f ! -name "*kccm*" -and ! -name "*ext*" -and ! -name "OWNERS"); do
    cluster_kccm_template=${cluster_template%%.*}-kccm.yaml
    cp -f $cluster_template ${cluster_kccm_template}
    echo "---" >> ${cluster_kccm_template}
    cat $kccm_template >> ${cluster_kccm_template}
done
