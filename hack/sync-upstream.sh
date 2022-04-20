#!/bin/bash -ex

set -e


# This script should be executed from within your fork of capk
#
# The script will...
# 1. automatically create remote branches for upstream and the openshift capk fork
# 2. create a new branch using the latest openshift capk fork's main
# 3. Merge upstream/main into the new branch, which layers in any upstream changes.
# 4. automatically resolve conflicts for expected file changes
# 5. validate upstream and downstream diff only contains expected file changes
# 6. push the new branch and automatically create a PR.
# 
# The script assumes the the gh cli is installed and authorized (download from here: https://cli.github.com/).
# The gh cli is required in order to auto create the PR

UPSTREAM_REMOTE="auto-sync-script-upstream"
OPENSHIFT_REMOTE="auto-sync-script-openshift-fork"

UPSTREAM_BRANCH="${UPSTREAM_REMOTE}/main"
OPENSHIFT_BRANCH="${OPENSHIFT_REMOTE}/main"

TODAY=$(date -u +%Y-%m-%d-%H-%M)
NEW_BRANCH_NAME="auto_sync_upstream_${TODAY}"

function verify_merge() {
	echo "Verifying merge only has expected differences with upstream branch"

	git checkout  $NEW_BRANCH_NAME

	DS_FILES=(OWNERS hack/sync-upstream.sh)
	diff=$(git diff upstream/main --stat | awk '{ print $1 }')

	for file in ${diff[@]}; do
	        # This if statment skips the final row of the diff --stat
	        # that is just printing out the number of files that changed
	        if [ "${#DS_FILES[@]}" = "$file" ]; then
	                continue
	        fi

	        verified="false"
	        for ds_file in ${DS_FILES[@]}; do
	                if [ "$ds_file" = "$file" ]; then
	                        verified="true"
	                fi
	        done
	        if [ "$verified" = "false" ]; then
	                echo "Unexpected diff, file $file should not be different from upstream"
	                exit 1
	        fi

	        echo "verified diff file $file is allowed"
	done
}

DS_FILES=(OWNERS hack/sync-upstream.sh)

set +e
git remote add $UPSTREAM_REMOTE https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt.git
git remote add $OPENSHIFT_REMOTE git@github.com:openshift/cluster-api-provider-kubevirt.git
set -e

git fetch $UPSTREAM_REMOTE
git fetch $OPENSHIFT_REMOTE

git checkout $OPENSHIFT_BRANCH
git checkout -b $NEW_BRANCH_NAME

# backup the downstream files
for ds_file in ${DS_FILES[@]}; do
  cp ${ds_file} "${ds_file}.untracked"
done

set +e
git merge $UPSTREAM_BRANCH
set -e

# restore the downstream files, if they are conflicting
for ds_file in ${DS_FILES[@]}; do
  mv "${ds_file}.untracked" ${ds_file}
  git add ${ds_file}
done

git -c core.editor=true merge --continue

verify_merge

git push origin $NEW_BRANCH_NAME

# the gh CLI does not support choosing the remote repository from command line. Setting the default one
sed -i "/gh-resolved = base/d" .git/config
sed -i "/^\[remote \"${OPENSHIFT_REMOTE}\"\]/a \        gh-resolved = base" .git/config

gh pr create -B main -t "auto sync ${TODAY}" -b "auto sync upstream ${TODAY}" -R openshift/cluster-api-provider-kubevirt

# remove the default target repository, so gh cli will ask next time
sed -i "/gh-resolved = base/d" .git/config

