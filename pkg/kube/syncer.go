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
	"fmt"
	"log"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
)

type SyncContext struct {
	KubeClient    kubernetes.Interface
	SkupperClient skupperclient.Interface
	Namespace     string
}

type SyncState struct {
	client                 SyncContext
	watchers               Watchers
	selector               string
	stopCh                 <-chan struct{}
	handler                SyncHandler
	siteWatcher            *SiteWatcher
	secretWatcher          *SecretWatcher
}

type SyncHandler interface {
	SiteEvent(key string, site *skupperv1alpha1.Site) error
	SecretEvent(key string, secret *corev1.Secret) error
}

func (s *SyncState) Start() error {
	s.siteWatcher = s.watchers.WatchSitesWithOptions(skupperSelector(s.selector), s.client.Namespace, s.handler.SiteEvent)
	s.secretWatcher = s.watchers.WatchSecrets(ListByLabelSelector(s.selector), s.client.Namespace, s.handler.SecretEvent)

	s.siteWatcher.Start(s.stopCh)
	s.secretWatcher.Start(s.stopCh)

	log.Printf("waiting for informer caches for %s to sync", s.selector)
	if ok := s.siteWatcher.Sync(s.stopCh); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
	}
	if ok := s.secretWatcher.Sync(s.stopCh); !ok {
		return fmt.Errorf("Failed to wait for server secret watcher to sync")
	}
	log.Printf("...caches for %s synced", s.selector)
	return nil
}

type Syncer struct {
	id                    string
	source                SyncState
	target                SyncState
	controller            *Controller
	stopCh                chan struct{}
}

type SourceHandler struct{
	secretSync SyncType
	siteSync   SyncType
}

func newSourceHandler(source *SyncState, target *SyncState) *SourceHandler {
	return &SourceHandler{
		secretSync: newSecretSync(source, target),
		siteSync:   newSiteSync(source, target),
	}
}

type TargetHandler struct{
	secretSync SyncType
	siteSync   SyncType
}

func newTargetHandler(source *SyncState, target *SyncState) *TargetHandler {
	return &TargetHandler{
		secretSync: newSecretSync(source, target),
		siteSync:   newSiteSync(source, target),
	}
}

func newSecretSync(source *SyncState, target *SyncState) *SecretSync {
	h := &SecretSync{
		source: source,
		target: target,
	}
	h.mapping.init()
	return h
}

func newSiteSync(source *SyncState, target *SyncState) *SiteSync {
	h := &SiteSync{
		source: source,
		target: target,
	}
	h.mapping.init()
	return h
}

func getAnnotation(annotations map[string]string, key string) string {
	log.Printf("Getting annotation for %s in %v", key, annotations)
	if annotations == nil {
		return ""
	}
	return annotations[key]
}

type Mapping struct {
	sourceToTarget map[string]string
	targetToSource map[string]string
}

func (m *Mapping) init() {
	m.sourceToTarget = map[string]string{}
	m.targetToSource = map[string]string{}
}

func (m *Mapping) update(sourceKey string, targetKey string) {
	m.targetToSource[targetKey] = sourceKey
	m.sourceToTarget[sourceKey] = targetKey
}

func (m *Mapping) removeTarget(targetKey string) {
	if sourceKey, ok := m.source(targetKey); ok {
		m.removeSource(sourceKey)
	}
	delete(m.targetToSource, targetKey)
}

func (m *Mapping) removeSource(sourceKey string) {
	if targetKey, ok := m.source(sourceKey); ok {
		m.removeTarget(targetKey)
	}
	delete(m.sourceToTarget, sourceKey)
}

func (m *Mapping) target(sourceKey string) (string, bool) {
	targetKey, ok := m.sourceToTarget[sourceKey]
	return targetKey, ok
}

func (m *Mapping) source(targetKey string) (string, bool) {
	sourceKey, ok := m.targetToSource[targetKey]
	return sourceKey, ok
}

type SyncType interface {
	DeleteTarget(targetKey string) error
	CreateTarget(targetKey string, source interface{}) error
	Reconcile(target interface{}, source interface{}) error
	GetTargetAnnotation(source interface{}) string
	GetSource(sourceKey string) (interface{}, error)
	GetTarget(targetKey string) (interface{}, error)
	GetMapping() *Mapping
}

type SiteSync struct {
	mapping Mapping
	target  *SyncState
	source  *SyncState
}

func (s *SiteSync) GetMapping() *Mapping {
	return &s.mapping
}

func (s *SiteSync) GetTargetAnnotation(src interface{}) string {
	source := src.(*skupperv1alpha1.Site)
	return getAnnotation(source.ObjectMeta.Annotations, SyncTargetAnnotation)
}

func (s *SiteSync) DeleteTarget(targetKey string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(targetKey)
	if err != nil {
		return err
	}
	err = s.target.client.SkupperClient.SkupperV1alpha1().Sites(namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil && errors.IsNotFound(err) {
		return err
	}
	s.mapping.removeTarget(targetKey)
	return nil
}

func (s *SiteSync) CreateTarget(targetKey string, src interface{}) error {
	source := src.(*skupperv1alpha1.Site)
	namespace, name, err := cache.SplitMetaNamespaceKey(targetKey)
	if err != nil {
		return err
	}
	target := &skupperv1alpha1.Site {
		TypeMeta: metav1.TypeMeta{
			APIVersion: "skupper.io/v1alpha1",
			Kind:       "Site",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Annotations:     map[string]string{},
			Labels:          source.ObjectMeta.Labels,
		},
		Spec: source.Spec,
	}
	for k, v := range source.ObjectMeta.Annotations {
		if k != SyncTargetAnnotation {
			target.ObjectMeta.Annotations[k] = v
		}
	}

	_, err = s.target.client.SkupperClient.SkupperV1alpha1().Sites(namespace).Create(target)
	return err

}

func (s *SiteSync) Reconcile(tgt interface{}, src interface{}) error {
	target := tgt.(*skupperv1alpha1.Site)
	source := src.(*skupperv1alpha1.Site)
	if !reflect.DeepEqual(source.Spec, target.Spec) {
		target.Spec = source.Spec
		_, err := s.target.client.SkupperClient.SkupperV1alpha1().Sites(target.ObjectMeta.Namespace).Update(target)
		if err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(source.Status, target.Status) {
		source.Status = target.Status
		_, err := s.source.client.SkupperClient.SkupperV1alpha1().Sites(source.ObjectMeta.Namespace).UpdateStatus(source)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SiteSync) GetSource(sourceKey string) (interface{}, error) {
	o, e := s.source.siteWatcher.Get(sourceKey)
	if e != nil || o == nil {
		return nil, e
	}
	return o, nil
}

func (s *SiteSync) GetTarget(targetKey string) (interface{}, error) {
	o, e := s.target.siteWatcher.Get(targetKey)
	if e != nil || o == nil {
		return nil, e
	}
	return o, nil
}

type SecretSync struct {
	mapping Mapping
	target  *SyncState
	source  *SyncState
}

func (s *SecretSync) GetMapping() *Mapping {
	return &s.mapping
}

func (s *SecretSync) GetTargetAnnotation(src interface{}) string {
	source := src.(*corev1.Secret)
	return getAnnotation(source.ObjectMeta.Annotations, SyncTargetAnnotation)
}

func (s *SecretSync) DeleteTarget(targetKey string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(targetKey)
	if err != nil {
		return err
	}
	err = s.target.client.KubeClient.CoreV1().Secrets(namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil && errors.IsNotFound(err) {
		return err
	}
	s.mapping.removeTarget(targetKey)
	return nil
}

func (s *SecretSync) CreateTarget(targetKey string, src interface{}) error {
	source := src.(*corev1.Secret)
	namespace, name, err := cache.SplitMetaNamespaceKey(targetKey)
	if err != nil {
		return err
	}
	target := &corev1.Secret {
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Annotations:     source.ObjectMeta.Annotations,
			Labels:          source.ObjectMeta.Labels,
		},
		Data: source.Data,
		Type: source.Type,
	}
	delete(target.ObjectMeta.Annotations, SyncTargetAnnotation)
	if targetSite, ok := source.ObjectMeta.Annotations[TargetSiteAnnotation]; ok {
		site, _ := s.target.siteWatcher.Get(targetSite)
		if site != nil {
			target.ObjectMeta.OwnerReferences = getOwnerReferencesForSite(site)
		}
	}

	_, err = s.target.client.KubeClient.CoreV1().Secrets(namespace).Create(target)
	return err

}

func (s *SecretSync) Reconcile(tgt interface{}, src interface{}) error {
	target := tgt.(*corev1.Secret)
	source := src.(*corev1.Secret)
	if reflect.DeepEqual(source.Data, target.Data) {
		return nil
	}
	target.Data = source.Data
	_, err := s.target.client.KubeClient.CoreV1().Secrets(target.ObjectMeta.Namespace).Update(target)
	return err
}

func (s *SecretSync) GetSource(sourceKey string) (interface{}, error) {
	o, e := s.source.siteWatcher.Get(sourceKey)
	if e != nil || o == nil {
		return nil, e
	}
	return o, nil
}

func (s *SecretSync) GetTarget(targetKey string) (interface{}, error) {
	o, e := s.target.siteWatcher.Get(targetKey)
	if e != nil || o == nil {
		return nil, e
	}
	return o, nil
}

const (
	SyncerLabel string = "skupper.io/syncer"
	SyncTargetAnnotation string = "skupper.io/sync-target"
	TargetSiteAnnotation string = "skupper.io/target-site"
)

func (s *TargetHandler) targetDeleted(targetKey string, syncType SyncType) error {
	sourceKey, ok := syncType.GetMapping().source(targetKey)
	if !ok {
		return nil
	}
	source, err := syncType.GetSource(sourceKey)
	if err != nil || source == nil {
		return err
	}
	return syncType.CreateTarget(targetKey, source)
}

func (s *TargetHandler) targetEvent(targetKey string, target interface{}, syncType SyncType) error {
	sourceKey, ok := syncType.GetMapping().source(targetKey)
	if !ok {
		return nil
	}
	source, err := syncType.GetSource(sourceKey)
	if err != nil {
		return err
	}
	if source == nil {
		return syncType.DeleteTarget(targetKey)
	}
	return syncType.Reconcile(target, source)
}

func (s *TargetHandler) SiteEvent(key string, target *skupperv1alpha1.Site) error {
	log.Printf("Target site %q changed", key)
	if target == nil {
		return s.targetDeleted(key, s.siteSync)
	}
	return s.targetEvent(key, target, s.siteSync)
}

func (s *TargetHandler) SecretEvent(key string, target *corev1.Secret) error {
	if target == nil {
		return s.targetDeleted(key, s.secretSync)
	}
	return s.targetEvent(key, target, s.secretSync)
}

func (s *SourceHandler) sourceDeleted(sourceKey string, syncType SyncType) error {
	if targetKey, ok := syncType.GetMapping().target(sourceKey); ok {
		return syncType.DeleteTarget(targetKey)
	}
	return nil
}

func (s *SourceHandler) sourceEvent(sourceKey string, source interface{}, syncType SyncType) error {
	targetKey, ok := syncType.GetMapping().target(sourceKey)
	actualTargetKey := syncType.GetTargetAnnotation(source)
	if actualTargetKey == "" {
		log.Printf("Ignoring site with no target %q", sourceKey)
		return nil
	}
	if actualTargetKey != targetKey {
		if ok {
			// target has changed, delete previously mapped target
			err := syncType.DeleteTarget(targetKey)
			if err != nil {
				return err
			}
		}
		syncType.GetMapping().update(sourceKey, actualTargetKey)
		targetKey = actualTargetKey
	}

	target, err := syncType.GetTarget(targetKey)
	if err != nil {
		return err
	}
	if target == nil {
		return syncType.CreateTarget(targetKey, source)
	} else {
		return syncType.Reconcile(target, source)
	}
}

func (s *SourceHandler) SiteEvent(key string, source *skupperv1alpha1.Site) error {
	log.Printf("Source site %q changed", key)
	if source == nil {
		return s.sourceDeleted(key, s.siteSync)
	}
	return s.sourceEvent(key, source, s.siteSync)
}

func (s *SourceHandler) SecretEvent(key string, source *corev1.Secret) error {
	if source == nil {
		return s.sourceDeleted(key, s.secretSync)
	}
	return s.sourceEvent(key, source, s.secretSync)
}

func NewSyncer(id string, source SyncContext, target SyncContext) *Syncer {
	controller := NewController(id, source.KubeClient, source.SkupperClient)
	stopCh := make(chan struct{})
	syncer := &Syncer{
		id:     id,
		source: SyncState{
			client:   source,
			watchers: controller,
			selector: "skupper.io/syncer=" + id,
			stopCh:   stopCh,
		},
		target: SyncState{
			client:   target,
			watchers: controller.NewWatchers(target.KubeClient, target.SkupperClient),
			selector: "skupper.io/syncer=" + id,
			stopCh:   stopCh,
		},
		controller: controller,
		stopCh: stopCh,

	}
	syncer.source.handler = newSourceHandler(&syncer.source, &syncer.target)
	syncer.target.handler = newTargetHandler(&syncer.source, &syncer.target)
	return syncer
}

func (s *Syncer) Start() error {
	if err := s.source.Start(); err != nil {
		return err
	}
	if err := s.target.Start(); err != nil {
		return err
	}
	s.controller.Start(s.source.stopCh)
	log.Printf("Syncer for %s is now running", s.source.selector)

	return nil
}

func (s *Syncer) Stop() {
	close(s.stopCh)
}

func HasSyncTarget(o *metav1.ObjectMeta) bool {
	return getAnnotation(o.Annotations, SyncTargetAnnotation) != ""
}
