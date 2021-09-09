package data

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/types"
)

const (
	SiteQueryError      string = "SiteQueryError"
	SiteQueryRequest    string = "SiteQueryRequest"
	GatewayQueryError   string = "GatewayQueryError"
	GatewayQueryRequest string = "GatewayQueryRequest"
	ServiceCheckError   string = "ServiceCheckError"
	ServiceCheckRequest string = "ServiceCheckRequest"
)

type QueryContext interface {
	LookupSiteInfo(site *Site) error
	LookupServiceDefinition(address string, svc *ServiceDetail) error
	LookupIngressPorts(svc *ServiceDetail) error
}

type SiteQueryServer struct {
	agentPool   *qdr.AgentPool
	server      *qdr.RequestServer
	nameMapping NameMapping
	site        Site
	context     QueryContext
}

func NewSiteQueryServer(siteId string, context QueryContext, ipLookup NameMapping, factory *qdr.ConnectionFactory) *SiteQueryServer {
	sqs := SiteQueryServer{
		agentPool:   qdr.NewAgentPool(factory),
		nameMapping: ipLookup,
		context:     context,
	}
	sqs.server = qdr.NewRequestServer(getSiteQueryAddress(siteId), &sqs, sqs.agentPool)
	return &sqs
}

func (s *SiteQueryServer) getLocalSiteInfo() error {
	return s.context.LookupSiteInfo(&s.site)
}

func (s *SiteQueryServer) getLocalSiteQueryData() (*SiteQueryData, error) {
	err := s.getLocalSiteInfo()
	if err != nil {
		return nil, fmt.Errorf("Could not lookup site config: %s", err)
	}
	data := SiteQueryData{
		Site: s.site,
	}
	agent, err := s.agentPool.Get()
	if err != nil {
		return &data, fmt.Errorf("Could not get management agent: %s", err)
	}
	defer s.agentPool.Put(agent)

	routers, err := agent.GetAllRouters()
	if err != nil {
		return &data, fmt.Errorf("Error retrieving routers: %s", err)
	}
	err = getServiceInfo(agent, routers, &data, s.nameMapping)
	if err != nil {
		return &data, fmt.Errorf("Error getting local service info: %s", err)
	}
	return &data, nil
}

func (s *SiteQueryServer) getGatewayQueryData() ([]SiteQueryData, error) {
	results := []SiteQueryData{}
	agent, err := s.agentPool.Get()
	if err != nil {
		return results, fmt.Errorf("Could not get management agent: %s", err)
	}
	defer s.agentPool.Put(agent)

	gateways, err := agent.GetLocalGateways()
	if err != nil {
		return results, fmt.Errorf("Error retrieving gateways: %s", err)
	}
	for _, gateway := range gateways {
		data := SiteQueryData{
			Site: Site{
				SiteName:  qdr.GetSiteNameForGateway(&gateway),
				SiteId:    gateway.Site.Id,
				Version:   gateway.Site.Version,
				Connected: []string{s.site.SiteId},
				Edge:      true,
				Gateway:   true,
			},
		}
		err = getServiceInfoForRouters(agent, []qdr.Router{gateway}, &data, s.nameMapping)
		if err != nil {
			return results, fmt.Errorf("Error getting local service info: %s", err)
		}
		results = append(results, data)
	}
	return results, nil
}

func getSiteQueryAddress(siteId string) string {
	return siteId + "/skupper-site-query"
}

const (
	ServiceCheckQueryType string = "service-check"
	GatewayQueryQueryType string = "gateway-query"
)

func (s *SiteQueryServer) Request(request *qdr.Request) (*qdr.Response, error) {
	if request.Type == ServiceCheckQueryType {
		return s.HandleServiceCheck(request)
	} else if request.Type == GatewayQueryQueryType {
		return s.HandleGatewayQuery(request)
	} else {
		return s.HandleSiteQuery(request)
	}
}

func (s *SiteQueryServer) HandleSiteQuery(request *qdr.Request) (*qdr.Response, error) {
	//if request has explicit version, send SiteQueryData, else send LegacySiteData
	if request.Version == "" {
		event.Record(SiteQueryRequest, "legacy site data request")
		data := s.site.AsLegacySiteInfo()
		bytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("Could not encode response: %s", err)
		}
		return &qdr.Response{
			Version: types.Version,
			Body:    string(bytes),
		}, nil
	} else {
		event.Record(SiteQueryRequest, "site data request")
		data, err := s.getLocalSiteQueryData()
		if err != nil {
			return nil, fmt.Errorf("Could not get response: %s", err)
		}
		bytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("Could not encode response: %s", err)
		}
		return &qdr.Response{
			Version: types.Version,
			Body:    string(bytes),
		}, nil
	}
}

func (s *SiteQueryServer) HandleServiceCheck(request *qdr.Request) (*qdr.Response, error) {
	event.Recordf(ServiceCheckRequest, "checking service %s", request.Body)
	data, err := s.getServiceDetail(context.Background(), request.Body)
	if err != nil {
		return &qdr.Response{
			Version: types.Version,
			Type:    ServiceCheckError,
			Body:    err.Error(),
		}, nil
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("Could not encode service check response: %s", err)
	}
	return &qdr.Response{
		Version: types.Version,
		Type:    request.Type,
		Body:    string(bytes),
	}, nil
}

func (s *SiteQueryServer) HandleGatewayQuery(request *qdr.Request) (*qdr.Response, error) {
	event.Record(GatewayQueryRequest, "gateway request")
	data, err := s.getGatewayQueryData()
	if err != nil {
		return nil, fmt.Errorf("Could not get response: %s", err)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("Could not encode response: %s", err)
	}
	return &qdr.Response{
		Version: types.Version,
		Body:    string(bytes),
	}, nil

}

func (s *SiteQueryServer) run() {
	for {
		ctxt := context.Background()
		err := s.server.Run(ctxt)
		if err != nil {
			event.Recordf(SiteQueryError, "Error handling requests: %s", err)
		}
	}
}

func getTcpAddressFilter(address string) qdr.TcpEndpointFilter {
	return func(endpoint *qdr.TcpEndpoint) bool {
		return matchQualifiedAddress(address, endpoint.Address)
	}
}

func getHttpAddressFilter(address string) qdr.HttpEndpointFilter {
	return func(endpoint *qdr.HttpEndpoint) bool {
		return matchQualifiedAddress(address, endpoint.Address)
	}
}

func matchQualifiedAddress(unqualified string, qualified string) bool {
	return unqualified == strings.Split(qualified, ":")[0]
}

func (s *SiteQueryServer) getServiceDetail(context context.Context, address string) (ServiceDetail, error) {
	detail := ServiceDetail{
		SiteId: s.site.SiteId,
	}
	err := s.context.LookupServiceDefinition(address, &detail)
	if err != nil {
		return detail, err
	}
	err = s.context.LookupIngressPorts(&detail)
	if err != nil {
		return detail, err
	}

	agent, err := s.agentPool.Get()
	if err != nil {
		return detail, fmt.Errorf("Could not get management agent: %s", err)
	}
	defer s.agentPool.Put(agent)

	detail.LookupRouterBindings(agent)

	if len(detail.Definition.Targets) > 0 && len(detail.EgressBindings) == 0 {
		detail.AddObservation(fmt.Sprintf("No connectors on %s for %s ", detail.SiteId, detail.Definition.Address))
	}
	return detail, nil
}

func querySites(agent qdr.RequestResponse, sites []SiteQueryData) {
	for i, s := range sites {
		request := qdr.Request{
			Address: getSiteQueryAddress(s.SiteId),
			Version: types.Version,
		}
		response, err := agent.Request(&request)
		if err != nil {
			event.Recordf(SiteQueryError, "Request to %s failed: %s", s.SiteId, err)
		} else if response.Version == "" {
			//assume legacy version of site-query protocol
			info := LegacySiteInfo{}
			err := json.Unmarshal([]byte(response.Body), &info)
			if err != nil {
				event.Recordf(SiteQueryError, "Error parsing legacy json %q from %s: %s", response.Body, s.SiteId, err)
			} else {
				sites[i].SiteName = info.SiteName
				sites[i].Namespace = info.Namespace
				sites[i].Url = info.Url
				sites[i].Version = info.Version
			}
		} else {
			site := SiteQueryData{}
			err := json.Unmarshal([]byte(response.Body), &site)
			if err != nil {
				event.Recordf(SiteQueryError, "Error parsing json for site query %q from %s: %s", response.Body, s.SiteId, err)
			} else {
				sites[i].SiteName = site.SiteName
				sites[i].Namespace = site.Namespace
				sites[i].Url = site.Url
				sites[i].Version = site.Version
				sites[i].TcpServices = site.TcpServices
				sites[i].HttpServices = site.HttpServices
			}
		}
	}
}

func queryGateways(agent qdr.RequestResponse, sites []SiteQueryData) []SiteQueryData {
	gateways := []SiteQueryData{}
	for _, s := range sites {
		request := qdr.Request{
			Address: getSiteQueryAddress(s.SiteId),
			Version: types.Version,
			Type:    GatewayQueryQueryType,
		}
		response, err := agent.Request(&request)
		if err != nil {
			event.Recordf(GatewayQueryError, "Request to %s failed: %s", s.SiteId, err)
		} else {
			sites := []SiteQueryData{}
			err := json.Unmarshal([]byte(response.Body), &sites)
			if err != nil {
				event.Recordf(SiteQueryError, "Error parsing json for site query %q from %s: %s", response.Body, s.SiteId, err)
			}
			gateways = append(gateways, sites...)
		}
	}
	return gateways
}

func getServiceInfo(agent *qdr.Agent, network []qdr.Router, site *SiteQueryData, lookup NameMapping) error {
	return getServiceInfoForRouters(agent, qdr.GetRoutersForSite(network, site.SiteId), site, lookup)
}

func getServiceInfoForRouters(agent *qdr.Agent, routers []qdr.Router, site *SiteQueryData, lookup NameMapping) error {
	bridges, err := agent.GetBridges(routers)
	if err != nil {
		return fmt.Errorf("Error retrieving bridge configuration: %s", err)
	}
	httpRequestInfo, err := agent.GetHttpRequestInfo(routers)
	if err != nil {
		return fmt.Errorf("Error retrieving http request info: %s", err)
	}
	tcpConnections, err := agent.GetTcpConnections(routers)
	if err != nil {
		return fmt.Errorf("Error retrieving tcp connection info: %s", err)
	}
	site.HttpServices = GetHttpServices(site.SiteId, httpRequestInfo, qdr.GetHttpConnectors(bridges), qdr.GetHttpListeners(bridges), lookup)
	site.TcpServices = GetTcpServices(site.SiteId, tcpConnections, qdr.GetTcpConnectors(bridges), lookup)
	return nil
}

func checkServiceForSites(agent qdr.RequestResponse, address string, sites *ServiceCheck) error {
	details := []ServiceDetail{}
	for _, s := range sites.Details {
		request := qdr.Request{
			Address: getSiteQueryAddress(s.SiteId),
			Version: types.Version,
			Type:    ServiceCheckQueryType,
			Body:    address,
		}
		response, err := agent.Request(&request)
		if err != nil {
			event.Recordf(ServiceCheckError, "Request to %s failed: %s", s.SiteId, err)
			return err
		}
		if response.Type == ServiceCheckError {
			sites.AddObservation(fmt.Sprintf("%s on %s", response.Body, s.SiteId))
		} else {
			detail := ServiceDetail{}
			err = json.Unmarshal([]byte(response.Body), &detail)
			if err != nil {
				event.Recordf(ServiceCheckError, "Error parsing json for service check %q from %s: %s", response.Body, s.SiteId, err)
				return err
			}
			details = append(details, detail)
		}
	}
	sites.Details = details
	CheckService(sites)
	return nil
}
