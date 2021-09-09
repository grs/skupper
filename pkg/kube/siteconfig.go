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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	SiteConfigMapName   string = "skupper-site"
	SiteConfigEvent   string = "SiteConfig"
)

func GetSiteConfig(namespace string, cli kubernetes.Interface) (*types.SiteConfig, *metav1.ObjectMeta, error) {
	cm, err := cli.CoreV1().ConfigMaps(namespace).Get(SiteConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	config := GetSiteConfigFromConfigMap(cm)
	return config, &cm.ObjectMeta, nil
}

func GetSiteConfigFromConfigMap(cm *corev1.ConfigMap) (*types.SiteConfig) {
	config := types.DefaultSiteConfig()
	if cm.Data != nil {
		config.ReadFromMap(cm.Data)
		config.ReadLabelsAndAnnotations(cm.ObjectMeta.Labels, cm.ObjectMeta.Annotations)
	}
	config.Id = string(cm.ObjectMeta.UID)
	if config.Name == "" {
		config.Name = cm.ObjectMeta.Namespace
	}
	return config
}

func CreateSiteConfig(namespace string, cli kubernetes.Interface, config *types.SiteConfig) error {
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: SiteConfigMapName,
		},
		Data: map[string]string{},
	}
	err := config.Verify()
	if err != nil {
		return err
	}
	config.WriteToMap(cm.Data)
	_, err = cli.CoreV1().ConfigMaps(namespace).Create(cm)
	return err
}

func configureResources(container *corev1.Container, settings *types.Tuning) {
	if settings.Cpu != "" || settings.Memory != "" {
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{},
		}
		if settings.Cpu != "" {
			cpu, err := resource.ParseQuantity(settings.Cpu)
			if err == nil {
				container.Resources.Requests[corev1.ResourceCPU] = cpu
			} else {
				event.Recordf(SiteConfigEvent, "Invalid value for cpu: %s", err)
			}
		}
		if settings.Memory != "" {
			memory, err := resource.ParseQuantity(settings.Memory)
			if err == nil {
				container.Resources.Requests[corev1.ResourceMemory] = memory
			} else {
				event.Recordf(SiteConfigEvent, "Invalid value for memory: %s", err)
			}
		}
	}
}

func configureAffinity(settings *types.Tuning, podspec *corev1.PodSpec) {
	nodeSelector := utils.LabelToMap(settings.NodeSelector)
	affinity := utils.LabelToMap(settings.Affinity)
	antiAffinity := utils.LabelToMap(settings.AntiAffinity)
	if len(nodeSelector) > 0 {
		podspec.NodeSelector = nodeSelector
	}

	if len(affinity) > 0 || len(antiAffinity) > 0 {
		podspec.Affinity = &corev1.Affinity{}
		if len(affinity) > 0 {
			podspec.Affinity.PodAffinity = &corev1.PodAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: affinity,
							},
							TopologyKey: "kubernetes.io/hostname",
						},
					},
				},
			}
		}
		if len(antiAffinity) > 0 {
			podspec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: antiAffinity,
							},
							TopologyKey: "kubernetes.io/hostname",
						},
					},
				},
			}
		}
	}
}
