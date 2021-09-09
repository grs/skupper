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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func oauthProxyContainer(serviceAccount string, privatePort int32, publicPort int32, mountPath string) *corev1.Container {
	return &corev1.Container{
		Image: "openshift/oauth-proxy:latest",
		Name:  "oauth-proxy",
		Args:  getOauthProxyArgs(serviceAccount, privatePort, publicPort, mountPath),
		Ports: []corev1.ContainerPort{
			{
				Name:          "https",
				ContainerPort: publicPort,
			},
		},
	}
}

func addOauthProxySidecar(deployment *appsv1.Deployment, serviceAccount string, privatePort int32, publicPort int32, secret string, mountPath string) {
	name := "proxy-" + secret
	certDir := mountPath + "/" + name
	container := oauthProxyContainer(serviceAccount, privatePort, publicPort, certDir)
	deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secret,
			},
		},
	})
	container.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      name,
			MountPath: certDir,
		},
	}
	deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, *container)
}

func suffixes() []string {
	return []string{"first", "second", "third", "fourth"}
}

func addRedirectAnnotation(annotations map[string]string, routeName string) {
	key := "serviceaccounts.openshift.io/oauth-redirectreference." + suffixes()[len(annotations)]
	value := "{\"kind\":\"OAuthRedirectReference\",\"apiVersion\":\"v1\",\"reference\":{\"kind\":\"Route\",\"name\":\"" + routeName + "\"}}"
	annotations[key] = value

}

/*
func startOauthProxy(serviceAccount string, privatePort int32, publicPort int32, mountPath string) {
	oauthproxy.Start(getOauthProxyArgs(serviceAccount, privatePort, publicPort, mountPath))
}
*/

func getOauthProxyArgs(serviceAccount string, privatePort int32, publicPort int32, mountPath string) []string {
	return []string{
		"--https-address=:" + strconv.Itoa(int(publicPort)),
		"--provider=openshift",
		"--openshift-service-account=" + serviceAccount,
		"--upstream=http://localhost:" + strconv.Itoa(int(privatePort)),
		"--tls-cert=" + mountPath + "/tls.crt",
		"--tls-key=" + mountPath + "/tls.key",
		"--cookie-secret=SECRET",
	}
}
