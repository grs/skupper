package client

import (
	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/pkg/images"
	corev1 "k8s.io/api/core/v1"
	"os"
)

func GetRouterImageDetails() types.ImageDetails {
	return types.ImageDetails{
		Name:       images.GetRouterImageName(),
		PullPolicy: images.GetRouterImagePullPolicy(),
	}
}

func addRouterImageOverrideToEnv(env []corev1.EnvVar) []corev1.EnvVar {
	result := env
	image := os.Getenv(images.RouterImageEnvKey)
	if image != "" {
		result = append(result, corev1.EnvVar{Name: images.RouterImageEnvKey, Value: image})
	}
	policy := os.Getenv(images.RouterPullPolicyEnvKey)
	if policy != "" {
		result = append(result, corev1.EnvVar{Name: images.RouterPullPolicyEnvKey, Value: policy})
	}
	return result
}

func GetServiceControllerImageDetails() types.ImageDetails {
	return types.ImageDetails{
		Name:       images.GetServiceControllerImageName(),
		PullPolicy: images.GetServiceControllerImagePullPolicy(),
	}
}

func GetConfigSyncImageDetails() types.ImageDetails {
	return types.ImageDetails{
		Name:       images.GetConfigSyncImageName(),
		PullPolicy: images.GetConfigSyncImagePullPolicy(),
	}
}

func GetRouterImageName() string {
	return images.GetRouterImagePullPolicy()
}

func GetServiceControllerImageName() string {
	return images.GetServiceControllerImageName()
}

func GetConfigSyncImageName() string {
	return images.GetConfigSyncImageName()
}

func GetConfigSyncImagePullPolicy() string {
	return images.GetConfigSyncImagePullPolicy()
}
