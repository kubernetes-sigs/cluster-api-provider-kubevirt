#!/bin/bash -xe
config=tcprobe.yaml
repo=$(cat $config | yq -r .repo)
cri=$(cat $config | yq -r .cri)
image=$repo/tcprobe
$cri build . -t $image
$cri push $image
