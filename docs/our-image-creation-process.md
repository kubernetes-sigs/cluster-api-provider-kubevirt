Our image creation process is based on kubernetes-sigs/image-builder project based on packer.
We added a kubevirt target that creates an ubuntu kubevirt image.

This is currently(3.may.22) added in this PR: https://github.com/kubernetes-sigs/image-builder/pull/847

*Clone this PR
*cd into images/capi
*Run "make build-kubevirt-qemu-ubuntu-2004"