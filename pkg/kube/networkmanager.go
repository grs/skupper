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
	"log"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skupperproject/skupper/api/types"
	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	"github.com/skupperproject/skupper/pkg/certs"
)

type NetworkSite struct {
	key          string
	site         *skupperv1alpha1.Site
	serverSecret *corev1.Secret
	links        map[string]*corev1.Secret
	manager      *NetworkManager
	linkedTo     map[string]*NetworkSite
}

func newNetworkSite(key string, site *skupperv1alpha1.Site, manager *NetworkManager) *NetworkSite {
	return &NetworkSite{
		key:      key,
		site:     site,
		links:    map[string]*corev1.Secret{},
		manager:  manager,
		linkedTo: map[string]*NetworkSite{},
	}
}

type NetworkManager struct {
	client           kubernetes.Interface
	skupperClient    skupperclient.Interface
	sites            map[string]*NetworkSite
	ca               *corev1.Secret
	name             string
	namespace        string
	namespaceScoped  bool
}

func NewNetworkManager(client kubernetes.Interface, skupperClient skupperclient.Interface, name string, namespace string, namespaceScoped bool) *NetworkManager {
	return &NetworkManager{
		client:          client,
		skupperClient:   skupperClient,
		sites:           map[string]*NetworkSite{},
		name:            name,
		namespace:       namespace,
		namespaceScoped: namespaceScoped,
	}
}

func (m *NetworkManager) GetSite(key string, site *skupperv1alpha1.Site) *NetworkSite {
	if ns, ok := m.sites[key]; ok && ns != nil {
		ns.site = site
		return ns
	}
	ns := newNetworkSite(key, site, m)
	m.sites[key] = ns
	return ns
}

func (m *NetworkManager) SetCA(ca *corev1.Secret) bool {
	if m.ca != nil && reflect.DeepEqual(m.ca.Data, ca.Data) {
		return false
	}
	m.ca = ca
	return true
}

func (m *NetworkManager) RegenerateAll() error {
	for _, site := range m.sites {
		err := site.GenerateServerSecret()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *NetworkManager) RegenerateMissing() error {
	for _, site := range m.sites {
		if !site.HasServerSecret() {
			err := site.GenerateServerSecret()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *NetworkManager) GenerateCA() error {
	ca := certs.GenerateCASecret(m.name, m.name)
	err := m.ensureSecret(m.namespace, &ca)
	if err != nil {
		return err
	}
	if m.SetCA(&ca) {
		return m.RegenerateAll()
	}
	return nil
}

func (m *NetworkManager) ReadCA() error {
	ca, err := m.client.CoreV1().Secrets(m.namespace).Get(m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if m.SetCA(ca) {
		return m.RegenerateAll()
	}
	return nil
}

func (m *NetworkManager) ReadOrGenerateCA() error {
	ca, err := m.client.CoreV1().Secrets(m.namespace).Get(m.name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return m.GenerateCA()
	} else if err != nil {
		return err
	}
	if m.SetCA(ca) {
		return m.RegenerateAll()
	}
	return nil
}

func (s *NetworkSite) HasServerSecret() bool {
	return s.serverSecret != nil
}

func (s *NetworkSite) getHosts() []string {
	var hosts []string
	for _, a := range s.site.Status.Addresses {
		hosts = append(hosts, a.Host)
	}
	return hosts
}

func (s *NetworkSite) hasValidAddress() bool {
	for _, a := range s.site.Status.Addresses {
		if a.Host != "" && a.Port != "" {
			return true
		}
	}
	return false
}

func setSyncerLabelAndAnnotation(site *skupperv1alpha1.Site, secret *corev1.Secret, targetName string) {
	if siteTarget := getAnnotation(site.ObjectMeta.Annotations, SyncTargetAnnotation); siteTarget != "" {
		if secret.ObjectMeta.Labels == nil {
			secret.ObjectMeta.Labels = map[string]string{}
		}
		secret.ObjectMeta.Labels[SyncerLabel] = site.ObjectMeta.Labels[SyncerLabel]
		if secret.ObjectMeta.Annotations == nil {
			secret.ObjectMeta.Annotations = map[string]string{}
		}
		secret.ObjectMeta.Annotations[SyncTargetAnnotation] = strings.Split(siteTarget, "/")[0] + "/" + targetName
	}
}

func (s *NetworkSite) GenerateServerSecret() error {
	if s.manager.ca == nil {
		return nil
	}
	name := "skupper-site-server"
	hosts := s.getHosts()
	if len(hosts) == 0 {
		log.Printf("No valid addresses available yet for site %s", s.site.ObjectMeta.Name)
		return nil
	}
	secret := certs.GenerateSecret(name, s.site.ObjectMeta.Name, strings.Join(hosts, ","), s.manager.ca)
	secret.ObjectMeta.OwnerReferences = getOwnerReferencesForSite(s.site)
	if s.manager.namespaceScoped {
		secret.ObjectMeta.Name = s.site.ObjectMeta.Name
		setSyncerLabelAndAnnotation(s.site, &secret, "skupper-site-server")
	}
	err := s.ensureSecret(&secret)
	if err != nil {
		return err
	}
	s.serverSecret = &secret
	return nil
}

func (s *NetworkSite) linkTo(site *NetworkSite) error {
	token := site.generateLinkToken()
	if s.manager.namespaceScoped {
		setSyncerLabelAndAnnotation(s.site, token, token.ObjectMeta.Name)
	}
	err := s.ensureSecret(token)
	if err != nil {
		return err
	}
	s.linkedTo[site.key] = site
	return nil
}

func linked(a *NetworkSite, b *NetworkSite) bool {
	log.Printf("Checking linking of %s and %s", a.key, b.key)
	if _, ok := a.linkedTo[b.key]; ok {
		return true
	}
	if _, ok := b.linkedTo[a.key]; ok {
		return true
	}
	return false
}

func (s *NetworkSite) generateLinkToken() *corev1.Secret {
	if s.manager.ca == nil {
		return nil
	}
	name := string(s.site.ObjectMeta.UID)
	secret := certs.GenerateSecret(name, s.site.ObjectMeta.Name, "", s.manager.ca)
	secret.ObjectMeta.OwnerReferences = getOwnerReferencesForSite(s.site)
	secret.ObjectMeta.Annotations = map[string]string{
		//TODO: add version annotation (need version to be written into site status)
	}
	secret.ObjectMeta.Labels = map[string]string{
		types.SkupperTypeQualifier: types.TypeToken,
	}
	secret.ObjectMeta.Labels[types.SkupperTypeQualifier] = types.TypeToken
	for _, a := range s.site.Status.Addresses {
		secret.ObjectMeta.Annotations[a.Name+"-host"] = a.Host
		secret.ObjectMeta.Annotations[a.Name+"-port"] = a.Port
	}
	return &secret
}

func (m *NetworkManager) LinkAll() error {
	for _, a := range m.sites {
		err := m.Link(a)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *NetworkManager) Link(b *NetworkSite) error {
	log.Printf("Linking in %s", b.key)
	for key, a := range m.sites {
		if key != b.key && !linked(a, b) {
			if b.hasValidAddress() {
				err := a.linkTo(b)
				if err != nil {
					return err
				}
				log.Printf("Linked %s to %s", a.key, b.key)
			} else if a.hasValidAddress() {
				err := b.linkTo(a)
				if err != nil {
					return err
				}
				log.Printf("Linked %s to %s", b.key, a.key)
			} else {
				log.Printf("Cannot link %s and %s as neither has a valid address", a.key, b.key)
			}
		}
	}
	return nil
}

func (s *NetworkSite) ensureSecret(desired *corev1.Secret) error {
	return s.manager.ensureSecret(s.site.ObjectMeta.Namespace, desired)
}

func (m *NetworkManager) ensureSecret(namespace string, desired *corev1.Secret) error {
	actual, err := m.client.CoreV1().Secrets(namespace).Get(desired.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.CoreV1().Secrets(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
	if reflect.DeepEqual(actual.Data, desired.Data) {
		_, err = m.client.CoreV1().Secrets(namespace).Update(actual)
		if err != nil {
			return err
		}
	}
	return nil
}
