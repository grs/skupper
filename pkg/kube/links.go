package kube

import (
	"fmt"
	"strconv"
	"log"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	LinkManagement string = "LinkManagement"
)

type Connectors interface {
	getConnectorStatus() (map[string]qdr.ConnectorStatus, error)
}

type ConnectorManager struct {
	agentPool *qdr.AgentPool
}

func newConnectorManager(pool *qdr.AgentPool) *ConnectorManager {
	return &ConnectorManager{
		agentPool: pool,
	}
}

func (m *ConnectorManager) getConnectorStatus() (map[string]qdr.ConnectorStatus, error) {
	agent, err := m.agentPool.Get()
	if err != nil {
		return map[string]qdr.ConnectorStatus{}, fmt.Errorf("Could not get management agent: %s", err)
	}
	defer m.agentPool.Put(agent)
	return agent.GetLocalConnectorStatus()
}

type LinkManager struct {
	cli       kubernetes.Interface
	namespace string
	agentPool  *qdr.AgentPool
	connectors Connectors
	siteMgr   *SiteManager
}

func newLinkManager(cli kubernetes.Interface, namespace string, pool *qdr.AgentPool, siteMgr *SiteManager) *LinkManager {
	return &LinkManager{
		cli:        cli,
		namespace:  namespace,
		connectors: newConnectorManager(pool),
		siteMgr:   siteMgr,
	}
}

func isTokenOrClaim(s *corev1.Secret) (bool, bool) {
	if s.ObjectMeta.Labels != nil {
		if typename, ok := s.ObjectMeta.Labels[types.SkupperTypeQualifier]; ok {
			return typename == types.TypeToken, typename == types.TypeClaimRequest
		}
	}
	return false, false
}

func getLinkFromClaim(s *corev1.Secret) *types.LinkStatus {
	link := types.LinkStatus{
		Name:       s.ObjectMeta.Name,
		Configured: false,
		Created:    s.ObjectMeta.CreationTimestamp.Format(time.RFC3339),
	}
	if s.ObjectMeta.Annotations != nil {
		link.Url = s.ObjectMeta.Annotations[types.ClaimUrlAnnotationKey]
		if desc, ok := s.ObjectMeta.Annotations[types.StatusAnnotationKey]; ok {
			link.Description = "Failed to redeem claim: " + desc
		}
		if value, ok := s.ObjectMeta.Annotations[types.TokenCost]; ok {
			cost, err := strconv.Atoi(value)
			if err == nil {
				link.Cost = cost
			}
		}
	}
	return &link
}

func getLinkFromToken(s *corev1.Secret, connectors map[string]qdr.ConnectorStatus) *types.LinkStatus {
	link := types.LinkStatus{
		Name:       s.ObjectMeta.Name,
		Configured: true,
		Created:    s.ObjectMeta.CreationTimestamp.Format(time.RFC3339),
	}
	if status, ok := connectors[link.Name]; ok {
		link.Url = fmt.Sprintf("%s:%s", status.Host, status.Port)
		link.Cost = status.Cost
		link.Connected = status.Status == "SUCCESS"
		link.Description = status.Description
	}
	return &link
}

func getLinkStatus(secret *corev1.Secret, connectors map[string]qdr.ConnectorStatus) *types.LinkStatus {
	isToken, isClaim := isTokenOrClaim(secret)
	if isClaim {
		return getLinkFromClaim(secret)
	} else if isToken {
		return getLinkFromToken(secret, connectors)
	} else {
		return nil
	}
}

func (m *LinkManager) GetLinks() ([]types.LinkStatus, error) {
	links := []types.LinkStatus{}
	secrets, err := m.cli.CoreV1().Secrets(m.namespace).List(metav1.ListOptions{LabelSelector: "skupper.io/type in (connection-token, token-claim)"})
	if err != nil {
		return links, err
	}
	connectors, err := m.connectors.getConnectorStatus()
	if err != nil {
		event.Recordf(LinkManagement, "Failed to retrieve connector status: %s", err)
	}
	for _, secret := range secrets.Items {
		link := getLinkStatus(&secret, connectors)
		if link != nil {
			links = append(links, *link)
		}
	}
	return links, nil
}

func (m *LinkManager) GetLink(name string) (*types.LinkStatus, error) {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	connectors, err := m.connectors.getConnectorStatus()
	if err != nil {
		return nil, err
	}
	return getLinkStatus(secret, connectors), nil
}

func (m *LinkManager) DeleteLink(name string) (bool, error) {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) || (err == nil && !isToken(secret)) {
		return false, fmt.Errorf("No such link %q", name)
	} else if err != nil {
		return false, err
	}
	err = m.cli.CoreV1().Secrets(m.namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return false, err
	}
	event.Recordf(LinkManagement, "Deleted link %q", name)
	return true, nil
}

func isToken(secret *corev1.Secret) bool {
	typename, ok := secret.ObjectMeta.Labels[types.SkupperTypeQualifier]
	return ok && (typename == types.TypeClaimRequest || typename == types.TypeToken)
}

func (m *LinkManager) CreateLink(cost int, token []byte) error {
	s := json.NewYAMLSerializer(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme)
	var secret corev1.Secret
	_, _, err := s.Decode(token, nil, &secret)
	if err != nil {
		return fmt.Errorf("Could not parse connection token: %w", err)
	}
	secret.ObjectMeta.Name = generateConnectorName(m.namespace, m.cli)
	err = verify(&secret)
	if err != nil {
		return err
	}
	//TODO: check version compatibility

	if secret.ObjectMeta.Labels[types.SkupperTypeQualifier] == types.TypeClaimRequest {
		//can site handle claims?
		if !utils.IsValidFor(m.siteMgr.GetVersion(), "0.7.0") {
			return fmt.Errorf("Claims not supported. Site version is %s, require %s", m.siteMgr.GetVersion(), "0.7.0")
		}
	}
	if cost != 0 {
		if secret.ObjectMeta.Annotations == nil {
			secret.ObjectMeta.Annotations = map[string]string{}
		}
		secret.ObjectMeta.Annotations[types.TokenCost] = strconv.Itoa(cost)
	}
	err = m.siteMgr.createSecret(&secret)
	if err != nil {
		return err
	}
	event.Recordf(LinkManagement, "Created link %q", secret.ObjectMeta.Name)
	return nil
}


func generateConnectorName(namespace string, cli kubernetes.Interface) string {
	secrets, err := cli.CoreV1().Secrets(namespace).List(metav1.ListOptions{})
	max := 1
	if err == nil {
		connector_name_pattern := regexp.MustCompile("link([0-9]+)+")
		for _, s := range secrets.Items {
			count := connector_name_pattern.FindStringSubmatch(s.ObjectMeta.Name)
			if len(count) > 1 {
				v, _ := strconv.Atoi(count[1])
				if v >= max {
					max = v + 1
				}
			}

		}
	} else {
		log.Fatal("Could not retrieve token secrets:", err)
	}
	return "link" + strconv.Itoa(max)
}

func verify(secret *corev1.Secret) error {
	if secret.ObjectMeta.Labels == nil {
		secret.ObjectMeta.Labels = map[string]string{}
	}
	if _, ok := secret.ObjectMeta.Labels[types.SkupperTypeQualifier]; !ok {
		//deduce type from structire of secret
		if _, ok = secret.Data["tls.crt"]; ok {
			secret.ObjectMeta.Labels[types.SkupperTypeQualifier] = types.TypeToken
		} else if secret.ObjectMeta.Annotations != nil && secret.ObjectMeta.Annotations[types.ClaimUrlAnnotationKey] != "" {
			secret.ObjectMeta.Labels[types.SkupperTypeQualifier] = types.TypeClaimRequest
		}
	}
	switch secret.ObjectMeta.Labels[types.SkupperTypeQualifier] {
	case types.TypeToken:
		CertTokenDataFields := []string{"tls.key", "tls.crt", "ca.crt"}
		for _, name := range CertTokenDataFields {
			if _, ok := secret.Data[name]; !ok {
				return fmt.Errorf("Expected %s field in secret data", name)
			}
		}
	case types.TypeClaimRequest:
		if _, ok := secret.Data["password"]; !ok {
			return fmt.Errorf("Expected password field in secret data")
		}
		if secret.ObjectMeta.Annotations == nil || secret.ObjectMeta.Annotations[types.ClaimUrlAnnotationKey] == "" {
			return fmt.Errorf("Expected %s annotation", types.ClaimUrlAnnotationKey)
		}
	default:
		return fmt.Errorf("Secret is not a valid skupper token")
	}
	return nil
}
