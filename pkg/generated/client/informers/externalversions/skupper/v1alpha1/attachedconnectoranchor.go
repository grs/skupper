/*
Copyright 2021 The Skupper Authors.

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

// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"
	time "time"

	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	versioned "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	internalinterfaces "github.com/skupperproject/skupper/pkg/generated/client/informers/externalversions/internalinterfaces"
	v1alpha1 "github.com/skupperproject/skupper/pkg/generated/client/listers/skupper/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// AttachedConnectorAnchorInformer provides access to a shared informer and lister for
// AttachedConnectorAnchors.
type AttachedConnectorAnchorInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.AttachedConnectorAnchorLister
}

type attachedConnectorAnchorInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewAttachedConnectorAnchorInformer constructs a new informer for AttachedConnectorAnchor type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAttachedConnectorAnchorInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredAttachedConnectorAnchorInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredAttachedConnectorAnchorInformer constructs a new informer for AttachedConnectorAnchor type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAttachedConnectorAnchorInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SkupperV1alpha1().AttachedConnectorAnchors(namespace).List(context.TODO(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SkupperV1alpha1().AttachedConnectorAnchors(namespace).Watch(context.TODO(), options)
			},
		},
		&skupperv1alpha1.AttachedConnectorAnchor{},
		resyncPeriod,
		indexers,
	)
}

func (f *attachedConnectorAnchorInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredAttachedConnectorAnchorInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *attachedConnectorAnchorInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&skupperv1alpha1.AttachedConnectorAnchor{}, f.defaultInformer)
}

func (f *attachedConnectorAnchorInformer) Lister() v1alpha1.AttachedConnectorAnchorLister {
	return v1alpha1.NewAttachedConnectorAnchorLister(f.Informer().GetIndexer())
}
