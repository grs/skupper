/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kube

import (
	"bytes"
	"log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

type TokenGenerator func(name string, namespace string) (*corev1.Secret, error)

type NetworkManager struct {
	cluster    string
	namespace  string
	config     []byte
	defClient  kubernetes.Interface
	actClient  kubernetes.Interface
	generator  TokenGenerator
	sites      map[string]string //maps name of site resource to namepsace on local cluster
	configmaps *ConfigMapWatcher
	secrets    *SecretWatcher
}

func NewNetworkManager(generator TokenGenerator, actClient kubernetes.Interface, controller *Controller, stopCh <-chan struct{}, token *corev1.Secret) (*NetworkManager, error) {
	mgr := &NetworkManager {
		cluster:   token.ObjectMeta.Annotations["skupper.io/cluster"],
		namespace: token.ObjectMeta.Annotations["skupper.io/namespace"],
		config:    token.Data["kubeconfig"],
		actClient: actClient,
		sites:     map[string]string{},
		generator: generator,
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(mgr.config)
	if err != nil {
		log.Printf("invalid kubeconfig: %v", err)
		return nil, nil // Don't retry.
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Printf("could not get client for pull secret: %v", err)
		return nil, err
	}
	mgr.defClient = client
	watchers := controller.NewWatchers(client)
	log.Printf("NetworkManager watching %q on %q", mgr.namespace, mgr.cluster)
	mgr.configmaps = watchers.WatchConfigMaps(nil, mgr.namespace, mgr.configMapEvent)
	mgr.secrets = watchers.WatchSecrets(nil, mgr.namespace, mgr.secretEvent)
	mgr.configmaps.Start(stopCh)
	mgr.secrets.Start(stopCh)
	return mgr, nil
}

func (n *NetworkManager) Stop() {
	//TODO
}

func (n *NetworkManager) HasChanged(token *corev1.Secret) bool {
	return !bytes.Equal(token.Data["kubeconfig"], n.config)
}

func (n *NetworkManager) configMapEvent(key string, cm *corev1.ConfigMap) error {
	log.Printf("ConfigMap event for %s", key)
	if cm == nil {
		_, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			return err
		}
		if _, ok := n.sites[name]; ok {
			return n.siteDeleted(name)
		}
		return nil
	}
	typename := cm.ObjectMeta.Labels["skupper.io/type"]
	if typename == "site"{
		return n.siteEvent(key, cm)
	} else if typename == "link" {
		return n.linkEvent(key, cm)
	} else if typename == "services" {
		return n.servicesEvent(key, cm)
	} else {
		return nil
	}
}

func (n *NetworkManager) secretEvent(key string, s *corev1.Secret) error {
	log.Printf("Secret event for %s", key)
	link, err := n.configmaps.Get(key)
	if link == nil {
		return nil
	}
	target := link.ObjectMeta.Labels["skupper.io/to"]
	if namespace, ok := n.sites[target]; ok {
		//this is the target cluster
		//write copy into appropriate namespace
		token := secret(s.ObjectMeta.Name, s.ObjectMeta.Labels, s.ObjectMeta.Annotations, s.Data)
		_, err = n.actClient.CoreV1().Secrets(namespace).Create(token)
		return err
	}
	return nil
}

func secret(name string, labels map[string]string, annotations map[string]string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Data: data,
	}
}

func (n *NetworkManager) linkEvent(key string, link *corev1.ConfigMap) error {
	source := link.ObjectMeta.Labels["skupper.io/from"]
	if namespace, ok := n.sites[source]; ok {
		//ensure there is a token, create one if not
		token, err := n.secrets.Get(key)
		if token == nil {
			token, err = n.generator(link.ObjectMeta.Name, namespace)
			if err != nil {
				return err
			}
			_, err = n.defClient.CoreV1().Secrets(link.ObjectMeta.Namespace).Create(token)
			return err
		} else if err != nil {
			return err
		} else {
			//already exists
			return nil
		}
	}
	return nil
}

func (n *NetworkManager) siteEvent(key string, settings *corev1.ConfigMap) error {
	cluster := settings.ObjectMeta.Labels["skupper.io/cluster"]
	if cluster == n.cluster {
		namespace := settings.ObjectMeta.Labels["skupper.io/namespace"]
		log.Printf("Site Update for %s in %s", settings.ObjectMeta.Name, namespace)
		n.sites[settings.ObjectMeta.Name] = namespace
		_, err := NewConfigMap("skupper-site", &settings.Data, &settings.Labels, &settings.Annotations, nil, namespace, n.actClient)
		return err
	}
	return nil
}

func (n *NetworkManager) siteDeleted(name string) error {
	if namespace, ok := n.sites[name]; ok {
		err := n.actClient.CoreV1().ConfigMaps(namespace).Delete("skupper-site", &metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		delete(n.sites, name)
	}
	return nil
}

func (n *NetworkManager) servicesEvent(key string, link *corev1.ConfigMap) error {
	namespace, ok := link.ObjectMeta.Labels["skupper.io/namespace"]
	if !ok {
		log.Printf("Invalid services, no 'skupper.io/namespace' label on ConfigMap %q", link.ObjectMeta.Name)
		return nil // no point in retrying
	}
	actual, err := n.actClient.CoreV1().ConfigMaps(namespace).Get("skupper-services", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = NewConfigMap("skupper-services", &link.Data, nil, nil, nil, namespace, n.actClient)
		return err
	} else if err != nil {
		return err
	} else {
		actual.Data = link.Data
		_, err = n.actClient.CoreV1().ConfigMaps(namespace).Update(actual)
		return err
	}
}
