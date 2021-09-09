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
	"crypto/tls"
	"crypto/x509"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func GetTlsConfigFromSecret(name string, namespace string, cli kubernetes.Interface) (*tls.Config, error) {
	secret, err := cli.CoreV1().Secrets(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if secret.Data == nil {
		return nil, fmt.Errorf("Secret does not contain TLS credentials")
	}
	config := tls.Config {
		MinVersion:         tls.VersionTLS10,
		InsecureSkipVerify: false,
		RootCAs:            x509.NewCertPool(),
	}
	ca, ok := secret.Data["ca.crt"]
	if !ok {
		return nil, fmt.Errorf("Secret does not contain CA certificate")
	}
	config.RootCAs.AppendCertsFromPEM(ca)
	cert, ok := secret.Data["tls.crt"]
	if !ok {
		return nil, fmt.Errorf("Secret does not contain certificate")
	}
	key, ok := secret.Data["tls.key"]
	if !ok {
		return nil, fmt.Errorf("Secret does not contain key")
	}
	certificate, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	config.Certificates = []tls.Certificate{certificate}

	return &config, nil
}
