package advertiser

import (
	jsonencoding "encoding/json"
	"fmt"
	"log"
	"reflect"
	"time"

	amqp "github.com/interconnectedcloud/go-amqp"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	skupperinterfaces "github.com/skupperproject/skupper/pkg/generated/client/informers/externalversions/internalinterfaces"
	"github.com/skupperproject/skupper/pkg/kube"
	"github.com/skupperproject/skupper/pkg/messaging"
)

const (
	ServiceAdvertsAddress string = "mc/$skupper-service-adverts"
	ServiceAdvertsEvent   string = "service-adverts"
)

type ServiceAdvertiser struct {
	origin        string
	controller    *kube.Controller
	stopCh        chan struct{}
	skupperClient skupperclient.Interface
	namespace     string
	required      *kube.RequiredServiceWatcher
	provided      *kube.ProvidedServiceWatcher
	publisher     *Publisher
	subscriber    *Subscriber
}

func skupperSelector(selector string) skupperinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.LabelSelector = selector
	}
}

func NewServiceAdvertiser(kubeClient kubernetes.Interface, skupperClient skupperclient.Interface, namespace string, connectionFactory messaging.ConnectionFactory, origin string) *ServiceAdvertiser {
	advertiser := &ServiceAdvertiser{
		origin:        origin,
		controller:    kube.NewController("service-advertiser", kubeClient, skupperClient),
		stopCh:        make(chan struct{}),
		skupperClient: skupperClient,
		namespace:     namespace,
		publisher:     newPublisher(connectionFactory),
	}
	advertiser.required = advertiser.controller.WatchRequiredServicesWithOptions(skupperSelector("skupper.io/advertiser"), namespace, advertiser.requiredServiceEvent)
	advertiser.provided = advertiser.controller.WatchProvidedServicesWithOptions(skupperSelector("skupper.io/advertise"), namespace, advertiser.providedServiceEvent)
	advertiser.subscriber = newSubscriber(connectionFactory, advertiser.handleAdvertisement)
	return advertiser
}

func (s *ServiceAdvertiser) Start() error {
	log.Println("Starting Service Advertiser...")
	s.required.Start(s.stopCh)
	s.provided.Start(s.stopCh)

	if ok := s.required.Sync(s.stopCh); !ok {
		return fmt.Errorf("Failed to sync required services")
	}
	if ok := s.provided.Sync(s.stopCh); !ok {
		return fmt.Errorf("Failed to sync provided services")
	}
	s.controller.Start(s.stopCh)
	go s.publisher.run()
	go s.subscriber.run()
	log.Println("Service Advertiser started")

	return nil
}

func (s *ServiceAdvertiser) Stop() {
	close(s.stopCh)
}

func (s *ServiceAdvertiser) requiredServiceEvent(key string, svc *skupperv1alpha1.RequiredService) error {
	return nil
}

func (s *ServiceAdvertiser) providedServiceEvent(key string, svc *skupperv1alpha1.ProvidedService) error {
	log.Printf("Service Advertiser got ProvidedService event for %s", key)
	update := ServiceUpdate{
		origin:   s.origin,
		services: s.provided.List(),
	}
	s.publisher.updates <- update
	return nil
}

func (s *ServiceAdvertiser) isInLocallyDefinedGroup(svc *skupperv1alpha1.ProvidedService) bool {
	//TODO
	return true
}

func isFromOrigin(labels map[string]string, origin string) bool {
	if labels == nil {
		return false
	}
	if value, ok := labels["skupper.io/advertiser"]; ok {
		return value == origin
	}
	return false
}

func match(svc *skupperv1alpha1.ProvidedService, existing *skupperv1alpha1.RequiredService) bool {
	return svc.Spec.Address == existing.Spec.Address && reflect.DeepEqual(svc.Spec.Ports, existing.Spec.Ports)
}

func (s *ServiceAdvertiser) ensureRequiredServiceFor(svc *skupperv1alpha1.ProvidedService, origin string) error {
	existing, err := s.skupperClient.SkupperV1alpha1().RequiredServices(s.namespace).Get(svc.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		required := &skupperv1alpha1.RequiredService{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "skupper.io/v1alpha1",
				Kind:       "RequiredService",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: svc.ObjectMeta.Name,
				Labels: map[string]string{
					"skupper.io/advertiser":   origin,
					"skupper.io/servicegroup": svc.ObjectMeta.Labels["skupper.io/servicegroup"],
				},
			},
			Spec: skupperv1alpha1.RequiredServiceSpec{
				Address: svc.Spec.Address,
				Ports:   svc.Spec.Ports,
			},
		}
		_, err = s.skupperClient.SkupperV1alpha1().RequiredServices(s.namespace).Create(required)
		return err
	} else if err != nil {
		return err
	}
	if match(svc, existing) {
		return nil
	}
	existing.Spec.Address = svc.Spec.Address
	existing.Spec.Ports = svc.Spec.Ports
	_, err = s.skupperClient.SkupperV1alpha1().RequiredServices(s.namespace).Update(existing)
	return err
}

func (s *ServiceAdvertiser) handleAdvertisement(update ServiceUpdate) {
	if update.origin == s.origin {
		log.Printf("Ignoring advertisement from self (%s)", update.origin)
		return
	}
	log.Printf("Handling advertisement from %s", update.origin)
	err := s.updateRequiredServices(update)
	if err != nil {
		log.Printf("Error updating advertised services: %s", err)
		//TODO: retry?
	}
	err = s.removeStaleRequiredServices(update)
	if err != nil {
		log.Printf("Error removing stale advertised services: %s", err)
		//TODO: retry?
	}
}

func (s *ServiceAdvertiser) removeStaleRequiredServices(update ServiceUpdate) error {
	//TODO
	return nil
}

func (s *ServiceAdvertiser) updateRequiredServices(update ServiceUpdate) error {
	//1. filter out any provided services for groups not present in this site
	available := map[string]*skupperv1alpha1.ProvidedService{}
	for _, svc := range update.services {
		if s.isInLocallyDefinedGroup(svc) {
			available[svc.ObjectMeta.Name] = svc
		}
	}
	//2. get current set of required services for the origin in question
	actual := map[string]*skupperv1alpha1.RequiredService{}
	for _, svc := range s.required.List() {
		if isFromOrigin(svc.ObjectMeta.Labels, update.origin) {
			actual[svc.ObjectMeta.Name] = svc
		}
	}
	//3. add any required services in the update that are not in already present group
	for key, desired := range available {
		if existing, ok := actual[key]; ok {
			delete(actual, key)
			if !match(desired, existing) {
				err := s.ensureRequiredServiceFor(desired, update.origin)
				if err != nil {
					return err
				}
			}
		} else {
			err := s.ensureRequiredServiceFor(desired, update.origin)
			if err != nil {
				return err
			}
		}
	}
	//4. remove any required services that were already present but not in the update
	for _, stale := range actual {
		err := s.skupperClient.SkupperV1alpha1().RequiredServices(s.namespace).Delete(stale.ObjectMeta.Name, &metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

type ServiceUpdate struct {
	origin   string
	version  string
	services []*skupperv1alpha1.ProvidedService
}

type Publisher struct {
	connectionFactory messaging.ConnectionFactory
	updates           chan ServiceUpdate
	closed            bool
	client            messaging.Connection
	ticker            *time.Ticker
	request           *amqp.Message
}

func newPublisher(connectionFactory messaging.ConnectionFactory) *Publisher {
	return &Publisher{
		connectionFactory: connectionFactory,
		updates:           make(chan ServiceUpdate),
	}
}

func (s *Publisher) run() {
	log.Println("Service Advertiser's publisher is running")
	s.ticker = time.NewTicker(5 * time.Second)
	defer s.ticker.Stop()
	for !s.closed {
		err := s._send()
		if err != nil {
			log.Printf("Error sending out updates: %s", err.Error())
		}
	}
	log.Println("Service adverts stopped sending")

}

func (s *Publisher) _send() error {
	client, err := s.connectionFactory.Connect()
	if err != nil {
		return err
	}
	s.client = client
	log.Printf("Service adverts sender connection to %s established", s.connectionFactory.Url())
	defer client.Close()

	sender, err := client.Sender(ServiceAdvertsAddress)
	if err != nil {
		return err
	}

	defer sender.Close()

	for {
		select {
		case update := <-s.updates:
			msg, err := encode(&update)
			if err != nil {
				log.Printf("Failed to encode message for service adverts: %s", err.Error())
			} else {
				s.request = msg
			}

		case <-s.ticker.C:
		}
		if s.request != nil {
			log.Println("Sending advertisement")
			err = sender.Send(s.request)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Publisher) stop() {
	s.closed = true
	if s.client != nil {
		s.client.Close()
	}
}

type ServiceUpdateHandler func(update ServiceUpdate)

type Subscriber struct {
	connectionFactory messaging.ConnectionFactory
	closed            bool
	client            messaging.Connection
	handler           ServiceUpdateHandler
}

func newSubscriber(connectionFactory messaging.ConnectionFactory, handler ServiceUpdateHandler) *Subscriber {
	return &Subscriber{
		connectionFactory: connectionFactory,
		handler:           handler,
	}
}

func (c *Subscriber) run() {
	c.closed = false
	c.receive()
}

func (c *Subscriber) receive() {
	for !c.closed {
		err := c._receive()
		if err != nil {
			log.Printf("Error receiving updates: %s", err.Error())
		}
	}
	log.Println("Service adverts stopped receiving")
}

func (c *Subscriber) _receive() error {
	client, err := c.connectionFactory.Connect()
	if err != nil {
		return err
	}
	c.client = client
	log.Printf("Service adverts receiver connection to %s established", c.connectionFactory.Url())
	defer client.Close()

	receiver, err := client.Receiver(ServiceAdvertsAddress, 10)
	if err != nil {
		return err
	}

	defer receiver.Close()

	for {
		msg, err := receiver.Receive()
		log.Println("Advertisment received")
		if err != nil {
			return err
		}
		receiver.Accept(msg)
		update, err := decode(msg)
		if err != nil {
			log.Println(err.Error())
		} else {
			//reconcile against current RequiredServices
			c.handler(update)
		}
	}
	return nil
}

func encode(in *ServiceUpdate) (*amqp.Message, error) {
	encoded, err := jsonencoding.Marshal(in.services)
	if err != nil {
		return nil, err
	}

	var request amqp.Message
	var properties amqp.MessageProperties
	properties.Subject = "service-advert"
	request.Properties = &properties
	request.ApplicationProperties = make(map[string]interface{})
	request.ApplicationProperties["origin"] = in.origin
	request.ApplicationProperties["version"] = "1.0"

	request.Value = string(encoded)

	return &request, nil
}

func decode(msg *amqp.Message) (ServiceUpdate, error) {
	result := ServiceUpdate{}
	subject := msg.Properties.Subject
	if subject != "service-advert" {
		return result, fmt.Errorf("Service advert subject not valid: %s", subject)
	}
	origin, ok := msg.ApplicationProperties["origin"].(string)
	if !ok {
		return result, fmt.Errorf("Service advert origin not valid: %v", msg.ApplicationProperties["origin"])
	}
	result.origin = origin

	if version, ok := msg.ApplicationProperties["version"].(string); ok {
		result.version = version
	}

	encoded, ok := msg.Value.(string)
	if !ok {
		return result, fmt.Errorf("Service advert body not valid: %v", msg.Value)
	}

	err := jsonencoding.Unmarshal([]byte(encoded), &result.services)

	if err != nil {
		return result, err
	}

	return result, nil
}
