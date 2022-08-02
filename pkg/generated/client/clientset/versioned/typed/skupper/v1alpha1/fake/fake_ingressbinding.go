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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeIngressBindings implements IngressBindingInterface
type FakeIngressBindings struct {
	Fake *FakeSkupperV1alpha1
	ns   string
}

var ingressbindingsResource = schema.GroupVersionResource{Group: "skupper.io", Version: "v1alpha1", Resource: "ingressbindings"}

var ingressbindingsKind = schema.GroupVersionKind{Group: "skupper.io", Version: "v1alpha1", Kind: "IngressBinding"}

// Get takes name of the ingressBinding, and returns the corresponding ingressBinding object, and an error if there is any.
func (c *FakeIngressBindings) Get(name string, options v1.GetOptions) (result *v1alpha1.IngressBinding, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(ingressbindingsResource, c.ns, name), &v1alpha1.IngressBinding{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IngressBinding), err
}

// List takes label and field selectors, and returns the list of IngressBindings that match those selectors.
func (c *FakeIngressBindings) List(opts v1.ListOptions) (result *v1alpha1.IngressBindingList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(ingressbindingsResource, ingressbindingsKind, c.ns, opts), &v1alpha1.IngressBindingList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.IngressBindingList{ListMeta: obj.(*v1alpha1.IngressBindingList).ListMeta}
	for _, item := range obj.(*v1alpha1.IngressBindingList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested ingressBindings.
func (c *FakeIngressBindings) Watch(opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(ingressbindingsResource, c.ns, opts))

}

// Create takes the representation of a ingressBinding and creates it.  Returns the server's representation of the ingressBinding, and an error, if there is any.
func (c *FakeIngressBindings) Create(ingressBinding *v1alpha1.IngressBinding) (result *v1alpha1.IngressBinding, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(ingressbindingsResource, c.ns, ingressBinding), &v1alpha1.IngressBinding{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IngressBinding), err
}

// Update takes the representation of a ingressBinding and updates it. Returns the server's representation of the ingressBinding, and an error, if there is any.
func (c *FakeIngressBindings) Update(ingressBinding *v1alpha1.IngressBinding) (result *v1alpha1.IngressBinding, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(ingressbindingsResource, c.ns, ingressBinding), &v1alpha1.IngressBinding{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IngressBinding), err
}

// Delete takes name of the ingressBinding and deletes it. Returns an error if one occurs.
func (c *FakeIngressBindings) Delete(name string, options *v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteAction(ingressbindingsResource, c.ns, name), &v1alpha1.IngressBinding{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeIngressBindings) DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(ingressbindingsResource, c.ns, listOptions)

	_, err := c.Fake.Invokes(action, &v1alpha1.IngressBindingList{})
	return err
}

// Patch applies the patch and returns the patched ingressBinding.
func (c *FakeIngressBindings) Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1alpha1.IngressBinding, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(ingressbindingsResource, c.ns, name, pt, data, subresources...), &v1alpha1.IngressBinding{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IngressBinding), err
}
