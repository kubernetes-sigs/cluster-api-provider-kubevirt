#!/bin/bash -ex

DS_FILES=(OWNERS hack/sync-upstream.sh)
EXCLUDE_FILES=$(echo ${DS_FILES[@]} | tr ' ' '|')

US=($(git remote -v | grep -E $"^[^${TAB} ]*${TAB}.*kubernetes-sigs/cluster-api-provider-kubevirt.git" | cut -f1 | uniq))
UPSTREAM=${US[0]}
if [[ -z ${UPSTREAM} ]]; then
  git remote add upstream https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt.git
  UPSTREAM=upstream
fi

OS=($(git remote -v | grep -E $"^[^${TAB} ]*${TAB}.*openshift/cluster-api-provider-kubevirt.git" | cut -f1 | uniq))
OPENSHIFT=${OS[0]}
if [[ -z ${OPENSHIFT} ]]; then
  git remote add openshift https://github.com/openshift/cluster-api-provider-kubevirt.git
  OPENSHIFT=openshift
fi

git fetch ${UPSTREAM} main
git fetch ${OPENSHIFT} main

if [[ $(git diff ${OPENSHIFT}/main ${UPSTREAM}/main --name-only | grep -vE "${EXCLUDE_FILES}" | wc -l) -eq 0 ]]; then
  echo "no change found; aborting..."
  exit 0
fi

TODAY=$(date -u +%Y-%m-%d)
BRANCH_NAME="auto_sync_upstream_${TODAY}"
git checkout -b ${BRANCH_NAME}
git reset --hard "${OPENSHIFT}/main"

for ds_file in ${DS_FILES[@]}; do
  cp ${ds_file} "${ds_file}.untracked"
done

set +e
git rebase ${UPSTREAM}/main
set -e

for ds_file in ${DS_FILES[@]}; do
  mv "${ds_file}.untracked" ${ds_file}
  git add ${ds_file}
done

if [[ ! git -c core.editor=true rebase --continue ]]; then
  git rebase --abort
  echo sync failed
  exit 1
fi

git push -u origin ${BRANCH_NAME}
