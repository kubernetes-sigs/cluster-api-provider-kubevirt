#!/bin/bash -ex

# This script syncs the upstream repository kubernetes-sigs/cluster-api-provider-kubevirt, with the downstream
# repository openshift/cluster-api-provider-kubevirt. It preserved the downstream files by backup these files
# and restore them after the rebase.
#
# The script assumes 3 git remotes:
# * origin - points to the user fork
# * upstream - points to the kubernetes-sig organization
# * openshift - points to the openshift organization
# if there are no openshift or upstream remotes, the script add them
#
# also, the script assumes the the gh cli is installed and authorized (download from here: https://cli.github.com/).
#
DS_FILES=(OWNERS hack/sync-upstream.sh)
EXCLUDE_FILES=$(echo ${DS_FILES[@]} | tr ' ' '|')
# get github user name
GH_USER=$(gh auth status 2> >(grep "Logged in to github.com as" | sed -E "s/^.*Logged in to github.com as ([^ ]+).*$/\1/g"))

# find the origin remote - the user fork
OR=($(git remote -v | grep -E $"^[^${TAB} ]*${TAB}.*${GH_USER}/cluster-api-provider-kubevirt.git" | cut -f1 | uniq))
ORIGIN=${OR[0]}
if [[ -z ${ORIGIN} ]]; then
  echo "the script requires a fork to your github user"
  exit 1
fi

# find the upstream remote. if not exist - creates it
US=($(git remote -v | grep -E $"^[^${TAB} ]*${TAB}.*kubernetes-sigs/cluster-api-provider-kubevirt.git" | cut -f1 | uniq))
UPSTREAM=${US[0]}
if [[ -z ${UPSTREAM} ]]; then
  git remote add upstream https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt.git
  UPSTREAM=upstream
fi

# find the downstream remote, if not exists, create it
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

# backup the downstream files
for ds_file in ${DS_FILES[@]}; do
  cp ${ds_file} "${ds_file}.untracked"
done

set +e
git rebase ${UPSTREAM}/main | grep -E "^CONFLICT " > conflicts.txt
set -e

# restore the downstream files, if they are conflicting
for ds_file in ${DS_FILES[@]}; do
  if grep "Merge conflict in ${ds_file}$" conflicts.txt; then
    mv "${ds_file}.untracked" ${ds_file}
    git add ${ds_file}
  else
    rm "${ds_file}.untracked"
  fi
done

# continue rebase after fixing the conflicts
if ! git -c core.editor=true rebase --continue; then
  git rebase --abort
  echo sync failed
  exit 1
fi

rm conflicts.txt

# push the changes to own repository
git push -u ${ORIGIN} ${BRANCH_NAME}

# the gh CLI does not support choosing the remote repository from command line. Setting the default one
sed -i "/gh-resolved = base/d" .git/config
sed -i "/^\[remote \"${OPENSHIFT}\"\]/a \        gh-resolved = base" .git/config

gh pr create -B main -t "auto sync ${TODAY}" -b "auto sync upstream ${TODAY}" -R openshift/cluster-api-provider-kubevirt

# remove the default target repository, so gh cli will ask next time
sed -i "/gh-resolved = base/d" .git/config
