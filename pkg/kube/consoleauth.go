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
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type ConsoleAuth struct {
	controller *SecretController
	data       map[string]string
        lock       sync.RWMutex
}

func newConsoleAuth(client kubernetes.Interface, namespace string, stopCh <-chan struct{}) *ConsoleAuth {
	auth := ConsoleAuth{
		data: map[string]string{},
	}
	auth.controller = NewNamedSecretController("skupper-console-users", client, namespace, &auth)
	auth.controller.Start(stopCh)
	return &auth
}

func (c *ConsoleAuth) Handle(name string, secret *corev1.Secret) error {
	data := map[string]string{}
	for k, v := range secret.Data {
		data[k] = string(v)
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	c.data = data
	return nil
}

func (c *ConsoleAuth) Authenticate(user string, password string) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if actual, ok := c.data[user]; ok && actual == password {
		return true
	}
	return false
}
