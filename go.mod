module sigs.k8s.io/cluster-api-provider-kubevirt

go 1.16

require (
	cloud.google.com/go v0.60.0 // indirect
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-logr/logr v0.4.0
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.11.0
	github.com/pkg/errors v0.9.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0 // indirect
	golang.org/x/crypto v0.0.0-20211117183948-ae814b36b871
	golang.org/x/sys v0.0.0-20210831042530-f4d43177bf5e // indirect
	golang.org/x/tools v0.1.5 // indirect
	k8s.io/api v0.21.1
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/component-base v0.21.1
	k8s.io/klog/v2 v2.8.0
	kubevirt.io/client-go v0.36.0
	sigs.k8s.io/cluster-api v0.3.11-0.20210525210043-6c7878e7b4a9
	sigs.k8s.io/controller-runtime v0.9.0-beta.5
	sigs.k8s.io/kind v0.11.0
)

replace (
	bitbucket.org/ww/goautoneg => github.com/munnerz/goautoneg v0.0.0-20120707110453-a547fc61f48d
	github.com/openshift/api => github.com/openshift/api v0.0.0-20191219222812-2987a591a72c
	github.com/openshift/client-go => github.com/openshift/client-go v0.0.0-20210112165513-ebc401615f47
	k8s.io/client-go => k8s.io/client-go v0.21.1
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.20.2
	sigs.k8s.io/structured-merge-diff => sigs.k8s.io/structured-merge-diff v0.0.0-20190302045857-e85c7b244fd2
)
