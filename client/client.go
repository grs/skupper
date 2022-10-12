package client

import (
	"bytes"
	"strings"
	"time"

	routev1client "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"
	"github.com/skupperproject/skupper/api/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/skupperproject/skupper/pkg/kube"
)

var minimumCompatibleVersion = "0.8.0"
var defaultRetry = wait.Backoff{
	Steps:    100,
	Duration: 10 * time.Millisecond,
	Factor:   1.0,
	Jitter:   0.1,
}

// A VAN Client manages orchestration and communications with the network components
type VanClient struct {
	Namespace     string
	KubeClient    kubernetes.Interface
	RouteClient   *routev1client.RouteV1Client
	RestConfig    *restclient.Config
	DynamicClient dynamic.Interface
}

func (cli *VanClient) GetNamespace() string {
	return cli.Namespace
}

func (cli *VanClient) GetKubeClient() kubernetes.Interface {
	return cli.KubeClient
}

func (cli *VanClient) GetVersion(component string, name string) string {
	return kube.GetComponentVersion(cli.Namespace, cli.KubeClient, component, name)
}

func (cli *VanClient) GetMinimumCompatibleVersion() string {
	return minimumCompatibleVersion
}

func NewClient(namespace string, context string, kubeConfigPath string) (*VanClient, error) {
	c := &VanClient{}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeConfigPath != "" {
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}
	}
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		},
	)
	restconfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return c, err
	}
	restconfig.ContentConfig.GroupVersion = &schema.GroupVersion{Version: "v1"}
	restconfig.APIPath = "/api"
	restconfig.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}
	c.RestConfig = restconfig
	c.KubeClient, err = kubernetes.NewForConfig(restconfig)
	if err != nil {
		return c, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(restconfig)
	resources, err := dc.ServerResourcesForGroupVersion("route.openshift.io/v1")
	if err == nil && len(resources.APIResources) > 0 {
		c.RouteClient, err = routev1client.NewForConfig(restconfig)
		if err != nil {
			return c, err
		}
	}

	if namespace == "" {
		c.Namespace, _, err = kubeconfig.Namespace()
		if err != nil {
			return c, err
		}
	} else {
		c.Namespace = namespace
	}
	c.DynamicClient, err = dynamic.NewForConfig(restconfig)
	if err != nil {
		return c, err
	}

	return c, nil
}

func (cli *VanClient) GenerateSyncerPullSecret(name string, accountname string) (string, error) {
	return cli.generateSyncerSecret(name, accountname, "pull-secret")
}

func (cli *VanClient) GenerateSyncerPushSecret(name string, accountname string) (string, error) {
	return cli.generateSyncerSecret(name, accountname, "push-secret")
}

func (cli *VanClient) generateSyncerSecret(name string, accountname string, typeLabel string) (string, error) {
	account, err := cli.KubeClient.CoreV1().ServiceAccounts(cli.Namespace).Get(accountname, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var secretname string
	for _, s := range account.Secrets {
		if strings.HasPrefix(s.Name, accountname + "-token") {
			secretname = s.Name
		}
	}
	secret, err := cli.KubeClient.CoreV1().Secrets(cli.Namespace).Get(secretname, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	clusters := make(map[string]*clientcmdapi.Cluster)
	clusters["default-cluster"] = &clientcmdapi.Cluster{
		Server:                   cli.RestConfig.Host,
		CertificateAuthorityData: cli.RestConfig.CAData,
		CertificateAuthority:     cli.RestConfig.CAFile,
	}

	contexts := make(map[string]*clientcmdapi.Context)
	contexts["default-context"] = &clientcmdapi.Context{
		Cluster:   "default-cluster",
		Namespace: cli.Namespace,
		AuthInfo:  "default-user",
	}

	authinfos := make(map[string]*clientcmdapi.AuthInfo)
	authinfos["default-user"] = &clientcmdapi.AuthInfo{
		Token: string(secret.Data["token"]),
	}

	config := clientcmdapi.Config{
		Kind:           "Config",
		APIVersion:     "v1",
		Clusters:       clusters,
		Contexts:       contexts,
		CurrentContext: "default-context",
		AuthInfos:      authinfos,
	}
	err = clientcmdapi.FlattenConfig(&config)
	if err != nil {
		return "", err
	}
	data, err := clientcmd.Write(config)
	if err != nil {
		return "", err
	}
	secret = &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Labels:          map[string]string{
				"skupper.io/type": typeLabel,
			},
		},
		Data: map[string][]byte{
			"kubeconfig": data,
		},
	}

	s := json.NewYAMLSerializer(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme)
	var out bytes.Buffer
	err = s.Encode(secret, &out)
	if err != nil {
		return "", err
	}
	return out.String(), err
}

func (cli *VanClient) GetIngressDefault() string {
	if cli.RouteClient == nil {
		return types.IngressLoadBalancerString
	}
	return types.IngressRouteString
}
