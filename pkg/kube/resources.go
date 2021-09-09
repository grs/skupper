package kube

import (
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var ServiceResource = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "services",
}

var SecretResource = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "secret",
}

var ConfigMapResource = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "configmap",
}

var RouteResource = schema.GroupVersionResource{
	Group:    "route.openshift.io",
	Version:  "v1",
	Resource: "routes",
}

type RequiredResources map[schema.GroupVersionResource][]string

func (r RequiredResources) add(resourceType schema.GroupVersionResource, resourceName string) {
	r[resourceType] = append(r[resourceType], resourceName)
}

func (r RequiredResources) addSecret(name string) {
	r.add(SecretResource, name)
}

func (r RequiredResources) addService(name string) {
	r.add(ServiceResource, name)
}

func (r RequiredResources) addConfigMap(name string) {
	r.add(ConfigMapResource, name)
}

func (r RequiredResources) addIngress(name string) {
	r.add(IngressResource, name)
}

func (r RequiredResources) addRoute(name string) {
	r.add(RouteResource, name)
}

func (r RequiredResources) addHttpProxy(name string) {
	r.add(HttpProxyResource, name)
}

func (r RequiredResources) reconcile(client dynamic.Interface, namespace string) error {
	for resourceType, requiredNames := range r {
		surplus := map[string]string{}
		for _, name := range requiredNames {
			surplus[name] = name
		}
		list, err := client.Resource(resourceType).Namespace(namespace).List(metav1.ListOptions{LabelSelector: "internal.skupper.io/controlled=true"})
		if errors.IsUnauthorized(err) {
			continue
		} else if err != nil {
			return err
		}
		for _, obj := range list.Items {
			delete(surplus, obj.GetName())
		}
		for name, _ := range surplus {
			err = client.Resource(resourceType).Namespace(namespace).Delete(name, &metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
