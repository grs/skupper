package kube

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	oldtypes "github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/pkg/data"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	TokenManagement string = "TokenManagement"

	ClaimExpiration             string = "skupper.io/claim-expiration"
	ClaimsRemaining             string = "skupper.io/claims-remaining"
	ClaimsMade                  string = "skupper.io/claims-made"
	ClaimUrlAnnotationKey       string = "skupper.io/url"
	ClaimPasswordDataKey        string = "password"
	ClaimCaCertDataKey          string = "ca.crt"

	TypeToken                   string = "connection-token"
	TypeClaimRecord             string = "token-claim-record"
	TypeClaimRequest            string = "token-claim"
)

func getIntAnnotation(key string, s *corev1.Secret) *int {
	if value, ok := s.ObjectMeta.Annotations[key]; ok {
		result, err := strconv.Atoi(value)
		if err == nil {
			return &result
		}
	}
	return nil
}

func getClaimsRemaining(s *corev1.Secret) *int {
	return getIntAnnotation(ClaimsRemaining, s)
}

func getClaimsMade(s *corev1.Secret) *int {
	return getIntAnnotation(ClaimsMade, s)
}

func getClaimExpiration(s *corev1.Secret) *time.Time {
	if value, ok := s.ObjectMeta.Annotations[ClaimExpiration]; ok {
		result, err := time.Parse(time.RFC3339, value)
		if err == nil {
			return &result
		}
	}
	return nil
}

func isTokenRecord(s *corev1.Secret) bool {
	if s.ObjectMeta.Labels != nil {
		if typename, ok := s.ObjectMeta.Labels["skupper.io/type"]; ok {
			return typename == TypeClaimRecord
		}
	}
	return false
}

func getTokenState(s *corev1.Secret) data.TokenState {
	return data.TokenState{
		Name:            s.ObjectMeta.Name,
		ClaimsRemaining: getClaimsRemaining(s),
		ClaimsMade:      getClaimsMade(s),
		ClaimExpiration: getClaimExpiration(s),
		Created:         s.ObjectMeta.CreationTimestamp.Format(time.RFC3339),
	}
}

type TokenManager struct {
	cli       kubernetes.Interface
	namespace string
	siteMgr   *SiteManager
}

func NewTokenManager(cli kubernetes.Interface, namespace string, siteMgr *SiteManager) *TokenManager {
	return &TokenManager{
		cli:       cli,
		namespace: namespace,
		siteMgr:   siteMgr,
	}
}

func (m *TokenManager) GetTokens() ([]data.TokenState, error) {
	tokens := []data.TokenState{}
	secrets, err := m.cli.CoreV1().Secrets(m.namespace).List(metav1.ListOptions{LabelSelector: "skupper.io/type=token-claim-record"})
	if err != nil {
		return tokens, err
	}
	for _, s := range secrets.Items {
		tokens = append(tokens, getTokenState(&s))
	}
	return tokens, nil
}

func (m *TokenManager) GetToken(name string) (*data.TokenState, error) {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if !isTokenRecord(secret) {
		return nil, nil
	}
	token := getTokenState(secret)
	return &token, nil
}

func (m *TokenManager) DeleteToken(name string) (bool, error) {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	} else if !isTokenRecord(secret) {
		return false, nil
	}
	err = m.cli.CoreV1().Secrets(m.namespace).Delete(name, &metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	event.Recordf(TokenManagement, "Deleted token %q", name)
	return true, nil
}

func (m *TokenManager) GenerateToken(options *data.TokenOptions) (interface{}, error) {
	password := utils.RandomId(128)
	claim, err := m.TokenClaimCreate(context.Background(), "", []byte(password), options.Expiry, options.Uses)
	if err != nil {
		return nil, err
	}
	return claim, nil
}

func (m *TokenManager) DownloadClaim(name string) (interface{}, error) {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if !isTokenRecord(secret) {
		return nil, nil
	}
	password := secret.Data[ClaimPasswordDataKey]
	claim, err := m.TokenClaimTemplateCreate(context.Background(), name, password, name)
	return claim, err
}

func (m *TokenManager) TokenClaimCreate(ctx context.Context, name string, password []byte, expiry time.Duration, uses int) (*corev1.Secret, error) {
	if name == "" {
		id, err := uuid.NewUUID()
		if err != nil {
			return nil, err
		}
		name = id.String()
	}
	claim, err := m.TokenClaimTemplateCreate(ctx, name, password, name)
	if err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	record := corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				oldtypes.SkupperTypeQualifier: oldtypes.TypeClaimRecord,
			},
			Annotations: map[string]string{
				oldtypes.SiteVersion: types.Version,
			},
		},
		Data: map[string][]byte{
			oldtypes.ClaimPasswordDataKey: password,
		},
	}
	if expiry > 0 {
		expiration := time.Now().Add(expiry)
		record.ObjectMeta.Annotations[oldtypes.ClaimExpiration] = expiration.Format(time.RFC3339)
	}
	if uses > 0 {
		record.ObjectMeta.Annotations[oldtypes.ClaimsRemaining] = strconv.Itoa(uses)
	}
	err = m.siteMgr.createSecret(&record)
	if err != nil {
		return nil, err
	}

	return claim, nil
}

type HostPort struct {
	Host string
	Port int
}

func (m *TokenManager) resolveClaimsHostPort() (HostPort, error) {
	result := HostPort{}
	/*
	current, err := m.getRouterConfig()
	if err != nil {
		return result, err
	}
	if current.IsEdge() {
		return result, fmt.Errorf("Edge configuration cannot accept connections")
	}
	service, err := m.cli.CoreV1().Services(m.namespace).Get(oldtypes.ControllerServiceName, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	port := getClaimsPort(service)
	if port == 0 {
		return result, fmt.Errorf("Site cannot accept connections")
	}
	host := fmt.Sprintf("%s.%s", oldtypes.ControllerServiceName, m.namespace)
	localOnly := true
	ok, err := configureClaimHostFromRoutes(&host, m)
	if err != nil {
		return result, err
	} else if ok {
		// host configured from route
		result.Port = 443
		localOnly = false
	} else if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		result.Host = kube.GetLoadBalancerHostOrIp(service)
		localOnly = false
	} else if service.Spec.Type == corev1.ServiceTypeNodePort {
		host, err = m.getControllerIngressHost()
		if err != nil {
			return result, err
		}
		port, err = getClaimsNodePort(service)
		if err != nil {
			return result, err
		}
		result.Host = host
		result.Port = port
		localOnly = false
	} else if suffix := getContourProxyClaimsHostSuffix(m); suffix != "" {
		result.Host = strings.Join([]string{oldtypes.ClaimsIngressPrefix, m.namespace, suffix}, ".")
		result.Port = 443
		localOnly = false
	} else {
		ingressRoutes, err := kube.GetIngressRoutes(oldtypes.IngressName, m.namespace, m.KubeClient)
		if err != nil {
			return result, err
		}
		if len(ingressRoutes) > 0 {
			for _, route := range ingressRoutes {
				if route.ServicePort == int(oldtypes.ClaimRedemptionPort) {
					result.Host = route.Host
					result.Port = 443
					localOnly = false
					break
				}
			}
		}
		return result, nil
	}
        */
	return result, nil
}

func (m *TokenManager) TokenClaimTemplateCreate(ctx context.Context, name string, password []byte, recordName string) (*corev1.Secret, error) {
	hostport, err := m.resolveClaimsHostPort()
	url := fmt.Sprintf("https://%s:%d/%s", hostport.Host, hostport.Port, recordName)
	caSecret, err := m.cli.CoreV1().Secrets(m.namespace).Get(oldtypes.SiteCaSecret, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	claim := corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				oldtypes.SkupperTypeQualifier: oldtypes.TypeClaimRequest,
			},
			Annotations: map[string]string{
				oldtypes.ClaimUrlAnnotationKey: url,
				oldtypes.SiteVersion:           types.Version,
			},
		},
		Data: map[string][]byte{
			oldtypes.ClaimPasswordDataKey: password,
			oldtypes.ClaimCaCertDataKey:   caSecret.Data["tls.crt"],
		},
	}
	siteId := m.siteMgr.GetSiteId()
	if siteId != "" {
		claim.ObjectMeta.Annotations[oldtypes.TokenGeneratedBy] = siteId
	}
	return &claim, nil
}
