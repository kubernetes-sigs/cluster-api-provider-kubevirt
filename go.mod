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
	golang.org/x/crypto v0.0.0-20210220033148-5ea612d1eb83
	golang.org/x/sys v0.0.0-20210831042530-f4d43177bf5e // indirect
	golang.org/x/text v0.3.5 // indirect
	golang.org/x/tools v0.1.5 // indirect
	k8s.io/api v0.21.1
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/component-base v0.21.1
	k8s.io/klog/v2 v2.8.0
	kubevirt.io/api v0.0.0-20211117075245-c94ce62baf5a
	sigs.k8s.io/cluster-api v0.3.11-0.20210525210043-6c7878e7b4a9
	sigs.k8s.io/controller-runtime v0.9.0-beta.5
	sigs.k8s.io/kind v0.11.0
)

replace (
	k8s.io/client-go => k8s.io/client-go v0.21.1
	kubevirt.io/containerized-data-importer-api => github.com/kubevirt/containerized-data-importer-api v1.41.1-0.20211122202922-53cdcf82a0bb
)
