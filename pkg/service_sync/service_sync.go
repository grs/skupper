package service_sync

import (
	"context"
	jsonencoding "encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	amqp "github.com/interconnectedcloud/go-amqp"

	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	ServiceSyncServiceEvent string = "ServiceSyncServiceEvent"
	ServiceSyncSiteEvent    string = "ServiceSyncSiteEvent"
	ServiceSyncConnection   string = "ServiceSyncConnection"
	ServiceSyncError        string = "ServiceSyncError"
	serviceSyncSubjectV1    string = "service-sync-update"
	serviceSyncSubjectV2    string = "service-sync-update-v2"

	ServiceSyncAddress      string = "mc/$skupper-service-sync"
)

type UpdateHandler func(changed []types.ServiceInterface, deleted []string, origin string) error

type ServiceSync struct {
	origin             string
	handler            UpdateHandler
	connectionFactory  *qdr.ConnectionFactory
	outgoing           chan string
	incoming           chan ServiceUpdate
	updates            chan map[string]types.ServiceInterface
	byOrigin           map[string]map[string]types.ServiceInterface
	localServices      map[string]types.ServiceInterface
	byName             map[string]types.ServiceInterface
	heardFrom          map[string]time.Time
}

type ServiceUpdate struct {
	origin      string
	definitions map[string]types.ServiceInterface
}

func (c *ServiceSync) pareByOrigin(service string) {
	for _, origin := range c.byOrigin {
		if _, ok := origin[service]; ok {
			delete(origin, service)
			return
		}
	}
}

func getAddresses(services []types.ServiceInterface) []string {
	addresses := []string{}
	for _, service := range services {
		addresses = append(addresses, service.Address)
	}
	return addresses
}

func NewServiceSync(origin string, handler UpdateHandler, connectionFactory *qdr.ConnectionFactory) *ServiceSync {
	s := &ServiceSync {
		origin:  origin,
		handler: handler,
		connectionFactory: connectionFactory,
	}
	go s.runServiceSync()
	return s
}

func (c *ServiceSync) LocalDefinitionsUpdated(definitions map[string]types.ServiceInterface) {
	c.updates <- definitions
}

func (c *ServiceSync) serviceSyncDefinitionsUpdated(definitions map[string]types.ServiceInterface) {
	latest := make(map[string]types.ServiceInterface) // becomes c.localServices
	byName := make(map[string]types.ServiceInterface)
	var added []types.ServiceInterface
	var modified []types.ServiceInterface
	var removed []types.ServiceInterface

	for name, original := range definitions {
		service := types.ServiceInterface{
			Address:      original.Address,
			Protocol:     original.Protocol,
			Ports:        original.Ports,
			Origin:       original.Origin,
			Headless:     original.Headless,
			Labels:       original.Labels,
			Aggregate:    original.Aggregate,
			EventChannel: original.EventChannel,
			Targets:      []types.ServiceInterfaceTarget{},
		}
		if service.Origin != "" && service.Origin != "annotation" {
			if _, ok := c.byOrigin[service.Origin]; !ok {
				c.byOrigin[service.Origin] = make(map[string]types.ServiceInterface)
			}
			c.byOrigin[service.Origin][name] = service
		} else {
			latest[service.Address] = service
			// may have previously been tracked by origin
			c.pareByOrigin(service.Address)
		}
		byName[service.Address] = service
	}

	for _, def := range c.localServices {
		if _, ok := latest[def.Address]; !ok {
			removed = append(removed, def)
		} else if !reflect.DeepEqual(def, latest[def.Address]) {
			modified = append(modified, def)
		}
	}
	for _, def := range latest {
		if _, ok := c.localServices[def.Address]; !ok {
			added = append(added, def)
		}
	}

	if len(added) > 0 {
		event.Recordf(ServiceSyncServiceEvent, "Service interface(s) added %s", strings.Join(getAddresses(added), ","))
	}
	if len(removed) > 0 {
		event.Recordf(ServiceSyncServiceEvent, "Service interface(s) removed %s", strings.Join(getAddresses(removed), ","))
	}
	if len(modified) > 0 {
		event.Recordf(ServiceSyncServiceEvent, "Service interface(s) modified %s", strings.Join(getAddresses(modified), ","))
	}

	c.localServices = latest
	c.byName = byName

	encoded, err := encode(latest)
	if err != nil {
		event.Recordf(ServiceSyncError, "Failed to create json for service definition sync: %s", err.Error())
	} else {
		c.outgoing <- encoded
	}
}

func equivalentServiceDefinition(a *types.ServiceInterface, b *types.ServiceInterface) bool {
	if a.Protocol != b.Protocol || !reflect.DeepEqual(a.Ports, b.Ports) || a.EventChannel != b.EventChannel || a.Aggregate != b.Aggregate || !reflect.DeepEqual(a.Labels, b.Labels) {
		return false
	}
	if a.Headless == nil && b.Headless == nil {
		return true
	} else if a.Headless != nil && b.Headless != nil {
		if a.Headless.Name != b.Headless.Name || a.Headless.Size != b.Headless.Size || !reflect.DeepEqual(a.Headless.TargetPorts, b.Headless.TargetPorts) {
			return false
		} else {
			return true
		}
	} else {
		return false
	}
}

func (c *ServiceSync) ensureServiceInterfaceDefinitions(origin string, serviceInterfaceDefs map[string]types.ServiceInterface) {
	var changed []types.ServiceInterface
	var deleted []string

	c.heardFrom[origin] = time.Now()

	for _, def := range serviceInterfaceDefs {
		existing, ok := c.byName[def.Address]
		if !ok || (existing.Origin == origin && !equivalentServiceDefinition(&def, &existing)) {
			changed = append(changed, def)
		}
	}

	if _, ok := c.byOrigin[origin]; !ok {
		c.byOrigin[origin] = make(map[string]types.ServiceInterface)
	} else {
		current := c.byOrigin[origin]
		for name, _ := range current {
			if _, ok := serviceInterfaceDefs[name]; !ok {
				deleted = append(deleted, name)
			}
		}
	}

	for _, name := range deleted {
		delete(c.byOrigin[origin], name)
	}

	c.handler(changed, deleted, origin)
}


func (c *ServiceSync) removeStaleDefinitions() {
	var agedOrigins []string

	now := time.Now()

	for origin, _ := range c.byOrigin {
		var deleted []string

		if lastHeard, ok := c.heardFrom[origin]; ok {
			if now.Sub(lastHeard) >= 60*time.Second {
				agedOrigins = append(agedOrigins, origin)
				agedDefinitions := c.byOrigin[origin]
				for name, _ := range agedDefinitions {
					deleted = append(deleted, name)
				}
				if len(deleted) > 0 {
					c.handler([]types.ServiceInterface{}, deleted, origin)
				}
			}
		}
	}

	for _, originName := range agedOrigins {
		event.Recordf(ServiceSyncSiteEvent, "Service sync aged out service definitions from origin %s", originName)
		delete(c.heardFrom, originName)
		delete(c.byOrigin, originName)
	}
}

func decode(msg *amqp.Message) (ServiceUpdate, error) {
	result := ServiceUpdate{}
	subject := msg.Properties.Subject
	if utils.StringSliceContains([]string{serviceSyncSubjectV2}, subject) {
		return result, fmt.Errorf("Service sync subject not valid: %s", subject)
	}
	origin, ok := msg.ApplicationProperties["origin"].(string)
	if !ok {
		return result, fmt.Errorf("Service sync origin not valid: %v", msg.ApplicationProperties["origin"])
	}
	result.origin = origin
	encoded, ok := msg.Value.(string)
	if !ok {
		return result, fmt.Errorf("Service sync body not valid: %v", msg.Value)
	}

	defs := &types.ServiceInterfaceList{}
	err := defs.ConvertFrom(encoded)
	if err != nil {
		return result, err
	}
	for _, def := range *defs {
		def.Origin = origin
		result.definitions[def.Address] = def
	}

	return result, nil
}

func encode(localServices map[string]types.ServiceInterface) (string, error) {
	local := make([]types.ServiceInterface, len(localServices))

	for _, si := range localServices {
		local = append(local, si)
	}

	encoded, err := jsonencoding.Marshal(local)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (c *ServiceSync) runSender() {
	for {
		err := c.sender()
		if err != nil {
			event.Recordf(ServiceSyncError, "Error sending out updates: %s", err.Error())
		} else {
			break
		}
	}
}

func (c *ServiceSync) sender() error {
	var request amqp.Message
	var properties amqp.MessageProperties

	ctx := context.Background()
	client, err := c.connectionFactory.Connect()
	if err != nil {
		return err
	}
	event.Recordf(ServiceSyncConnection, "Service sync sender connection to %s established", c.connectionFactory.Url())
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	sender, err := session.NewSender(amqp.LinkTargetAddress(ServiceSyncAddress))
	if err != nil {
		return err
	}

	defer sender.Close(ctx)

	tickerSend := time.NewTicker(5 * time.Second)

	properties.Subject = serviceSyncSubjectV2
	request.Properties = &properties
	request.ApplicationProperties = make(map[string]interface{})
	request.ApplicationProperties["origin"] = c.origin
	request.ApplicationProperties["version"] = types.Version

	for {
		select {
		case update := <-c.outgoing:
			request.Value = update

		case <-tickerSend.C:
		}
		err = sender.Send(ctx, &request)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *ServiceSync) runReceiver() {
	for {
		err := c.receiver()
		if err != nil {
			event.Recordf(ServiceSyncError, "Error receiving updates: %s", err.Error())
		} else {
			break
		}
	}
}

func (c *ServiceSync) receiver() error {
	ctx := context.Background()
	client, err := c.connectionFactory.Connect()
	if err != nil {
		return err
	}
	event.Recordf(ServiceSyncConnection, "Service sync receiver connection to %s established", c.connectionFactory.Url())
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	receiver, err := session.NewReceiver(
		amqp.LinkSourceAddress(ServiceSyncAddress),
		amqp.LinkCredit(10),
	)
	if err != nil {
		return err
	}

	defer receiver.Close(ctx)

	for {
		msg, err := receiver.Receive(ctx)
		if err != nil {
			return err
		}
		msg.Accept()
		update, err := decode(msg)
		if err != nil {
			event.Record(ServiceSyncError, err.Error())
		} else if update.origin != c.origin {
			c.incoming <- update
		}
	}

	return nil
}

func (c *ServiceSync) update() {
	tickerAge := time.NewTicker(30 * time.Second)
	for {
		select {
		case update := <-c.incoming:
			c.ensureServiceInterfaceDefinitions(update.origin, update.definitions)

		case latest := <-c.updates:
			c.serviceSyncDefinitionsUpdated(latest)

		case <-tickerAge.C:
			c.removeStaleDefinitions()
		}
	}
}


func (c *ServiceSync) runServiceSync() {
	go c.runSender()
	go c.runReceiver()
	c.update()
}

