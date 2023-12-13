package site

import (
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skupperproject/skupper/pkg/utils"
)

type Reachability struct {
	zone              string
	reachableFrom     []string
}

func (o *Reachability) ReadFromEnv() {
	o.zone = os.Getenv("ZONE")
	reachableFrom := os.Getenv("REACHABLE_FROM")
	if reachableFrom != "" {
		o.reachableFrom = strings.Split(reachableFrom, ",")
	}
}

func (o *Reachability) ReadFromToken(token *corev1.Secret) {
	if zone, ok := getAnnotation(&token.ObjectMeta, "skupper.io/zone"); ok {
		o.zone = zone
	}
	if reachableFrom, ok := getAnnotation(&token.ObjectMeta, "skupper.io/reachability"); ok {
		o.reachableFrom = strings.Split(reachableFrom, ",")
	}
}

func (o *Reachability) WriteToToken(token *corev1.Secret) {
	if o.zone != "" {
		setAnnotation(&token.ObjectMeta, "skupper.io/zone", o.zone)
	}
	if len(o.reachableFrom) > 0 {
		setAnnotation(&token.ObjectMeta, "skupper.io/reachability", strings.Join(o.reachableFrom, ","))
	}
}

func (a *Reachability) CanReach(b *Reachability) bool {
	return utils.StringSliceContains(b.reachableFrom, a.zone) || a.zone == b.zone
}

func GetReachabilityFrom(token *corev1.Secret) *Reachability {
	r := &Reachability{}
	r.ReadFromToken(token)
	return r
}

func getAnnotation(object *metav1.ObjectMeta, key string) (string, bool) {
	if object.Annotations == nil {
		return "", false
	}
	value, ok := object.Annotations[key]
	return value, ok
}

func setAnnotation(object *metav1.ObjectMeta, key string, value string) {
	if object.Annotations == nil {
		object.Annotations = map[string]string{
			key: value,
		}
	} else {
		object.Annotations[key] = value
	}
}
