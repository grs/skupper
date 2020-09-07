package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/qdr"
)

type ConsoleServer struct {
	agentPool *qdr.AgentPool
	iplookup  *IpLookup
}

func newConsoleServer(cli *client.VanClient, config *tls.Config) *ConsoleServer {
	return &ConsoleServer{
		agentPool: qdr.NewAgentPool("amqps://skupper-messaging:5671", config),
		iplookup:  NewIpLookup(cli),
	}
}

func authenticate(dir string, user string, password string) bool {
	filename := path.Join(dir, user)
	file, err := os.Open(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("Failed to authenticate %s, no such user exists", user)
		} else {
			log.Printf("Failed to authenticate %s: %s", user, err)
		}
		return false
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Printf("Failed to authenticate %s: %s", user, err)
		return false
	}
	return string(bytes) == password
}

func authenticated(h http.Handler) http.Handler {
	dir := os.Getenv("METRICS_USERS")
	if dir != "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, _ := r.BasicAuth()

			if authenticate(dir, user, password) {
				h.ServeHTTP(w, r)
			} else {
				w.Header().Set("WWW-Authenticate", "Basic realm=skupper")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			}
		})
	} else {
		return h
	}
}

func (server *ConsoleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, err := server.agentPool.Get()
	if err != nil {
		log.Printf("Could not get management agent : %s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	data, err := getConsoleData(agent, server.iplookup)
	server.agentPool.Put(agent)
	if err != nil {
		log.Printf("Error retrieving console data: %s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		bytes, err := json.MarshalIndent(data, "", "    ")
		if err != nil {
			log.Printf("Error writing json: %s", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			fmt.Fprintf(w, string(bytes))
		}
	}
}

func (server *ConsoleServer) start(stopCh <-chan struct{}) error {
	err := server.iplookup.start(stopCh)
	go server.listen()
	return err
}

func (server *ConsoleServer) listen() {
	addr := ":8080"
	if os.Getenv("METRICS_PORT") != "" {
		addr = ":" + os.Getenv("METRICS_PORT")
	}
	if os.Getenv("METRICS_HOST") != "" {
		addr = os.Getenv("METRICS_HOST") + addr
	}
	log.Printf("Console server listening on %s", addr)
	http.Handle("/DATA", authenticated(server))
	http.Handle("/", authenticated(http.FileServer(http.Dir("/app/console/"))))
	log.Fatal(http.ListenAndServe(addr, nil))
}

type ServiceStats struct {
	Address  string          `json:"address"`
	Protocol string          `json:"protocol"`
	Targets  []ServiceTarget `json:"targets"`
}

type ServiceTarget struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	SiteId string `json:"site_id"`
}

type HttpRequestsHandledList []HttpRequestsHandled
type HttpRequestsReceivedList []HttpRequestsReceived

type HttpServiceStats struct {
	ServiceStats
	RequestsReceived HttpRequestsReceivedList `json:"requests_received"`
	RequestsHandled  HttpRequestsHandledList  `json:"requests_handled"`
}

type HttpRequestsReceived struct {
	SiteId   string                      `json:"site_id"`
	ByClient map[string]HttpRequestStats `json:"by_client"`
}

type HttpRequestsHandled struct {
	SiteId            string                      `json:"site_id"`
	ByServer          map[string]HttpRequestStats `json:"by_server"`
	ByOriginatingSite map[string]HttpRequestStats `json:"by_originating_site"`
}

type HttpRequestStats struct {
	Requests       int                         `json:"requests"`
	BytesIn        int                         `json:"bytes_in"`
	BytesOut       int                         `json:"bytes_out"`
	Details        map[string]int              `json:"details"`
	LatencyMax     int                         `json:"latency_max"`
	ByHandlingSite map[string]HttpRequestStats `json:"by_handling_site,omitempty"`
}

type TcpServiceStats struct {
	ServiceStats
	ConnectionsIngress SiteConnectionsList `json:"connections_ingress"`
	ConnectionsEgress  SiteConnectionsList `json:"connections_egress"`
}

type SiteConnectionsList []SiteConnections

type SiteConnections struct {
	SiteId      string                     `json:"site_id"`
	Connections map[string]ConnectionStats `json:"connections"`
}

type ConnectionStats struct {
	Id        string `json:"id"`
	StartTime uint64 `json:"start_time"`
	LastOut   uint64 `json:"last_out"`
	LastIn    uint64 `json:"last_in"`
	BytesIn   int    `json:"bytes_in"`
	BytesOut  int    `json:"bytes_out"`
	Client    string `json:"client,omitempty"`
	Server    string `json:"server,omitempty"`
}

type TcpServiceStatsMap map[string]TcpServiceStats
type HttpServiceStatsMap map[string]HttpServiceStats

func getHttpRequestDetails(in qdr.Record) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		i, ok := qdr.AsInt(v)
		if ok {
			out[k] = i
		}
	}
	return out
}

func max(a int, b int) int {
	if b > a {
		return b
	} else {
		return a
	}
}

func mergeCounts(a map[string]int, b map[string]int) {
	for k, v := range b {
		if s, ok := a[k]; ok {
			a[k] = s + v
		} else {
			a[k] = v
		}
	}
}

func (a *HttpRequestStats) merge(b *HttpRequestStats) {
	a.Requests += b.Requests
	a.BytesIn += b.BytesIn
	a.BytesOut += b.BytesOut
	a.LatencyMax = max(a.LatencyMax, b.LatencyMax)
	mergeCounts(a.Details, b.Details)
}

func mergeHttpRequestStats(a map[string]HttpRequestStats, b map[string]HttpRequestStats) {
	for k, v := range b {
		if s, ok := a[k]; ok {
			s.merge(&v)
			a[k] = s
		} else {
			a[k] = v
		}
	}
}

func getHttpRequestStats(in qdr.Record) map[string]HttpRequestStats {
	out := map[string]HttpRequestStats{}
	for k, v := range in {
		m, ok := v.(map[string]interface{})
		if ok {
			r := qdr.Record(m)
			hrs := HttpRequestStats{
				Requests:   r.AsInt("requests"),
				BytesIn:    r.AsInt("bytes_in"),
				BytesOut:   r.AsInt("bytes_out"),
				Details:    getHttpRequestDetails(r.AsRecord("details")),
				LatencyMax: r.AsInt("latency_max"),
			}
			byHandlingSite := r.AsRecord("by_handling_site")
			if byHandlingSite != nil {
				hrs.ByHandlingSite = getHttpRequestStats(byHandlingSite)
			}
			out[k] = hrs
		}
	}
	log.Printf("getHttpRequestsStats() => %#v\n", out)
	return out
}

/*
func (requests HttpRequestsHandledList) update(request *HttpRequestsHandled) HttpRequestsHandledList {
	for _, r := range requests {
		if r.SiteId == request.SiteId {
			mergeHttpRequestStats(r.ByServer, request.ByServer)
			mergeHttpRequestStats(r.ByOriginatingSite, request.ByOriginatingSite)
			return requests
		}
	}
	return append(requests, *request)
}

func (requests HttpRequestsReceivedList) update(request *HttpRequestsReceived) HttpRequestsReceivedList {
	for _, r := range requests {
		if r.SiteId == request.SiteId {
			mergeHttpRequestStats(r.ByClient, request.ByClient)
			return requests
		}
	}
	return append(requests, *request)
}

func (services HttpServiceStatsMap) update(address string, protocol string, target []ServiceTarget) {
	service, ok := services[address]
	if ok {
		if len(target) > 0 {
			service.Targets = append(service.Targets, target...)
		}
		services[address] = service
	} else {
		service = HttpServiceStats{
			ServiceStats: ServiceStats{
				Address:  address,
				Protocol: protocol,
				Targets:  target,
			},
		}
		services[address] = service
	}
}
*/

func getTargetName(connectorName string) string {
	parts := strings.Split(connectorName, "@")
	if len(parts) > 0 {
		return parts[0]
	} else {
		return connectorName
	}
}

func getHttpProtocol(protocolVersion string) string {
	if protocolVersion == "HTTP/2.0" {
		return "http2"
	} else {
		return "http"
	}
}

func getServiceStats(bridges []qdr.BridgeConfig, sites []Site, tcpconnections [][]qdr.TcpConnection, iplookup *IpLookup) []interface{} {
	tcpServices := TcpServiceStatsMap{}
	httpServices := HttpServiceStatsMap{}
	for _, b := range bridges {
		for _, c := range b.TcpConnectors {
			target := []ServiceTarget{
				ServiceTarget{
					Name:   iplookup.getPodName(c.Host),
					Target: getTargetName(c.Name),
					SiteId: c.SiteId,
				},
			}
			service, ok := tcpServices[c.Address]
			if ok {
				service.Targets = append(service.Targets, target...)
				tcpServices[c.Address] = service
			} else {
				service = TcpServiceStats{
					ServiceStats: ServiceStats{
						Address:  c.Address,
						Protocol: "tcp",
						Targets:  target,
					},
				}
				tcpServices[c.Address] = service
			}
		}
		for _, l := range b.TcpListeners {
			if _, ok := tcpServices[l.Address]; !ok {
				tcpServices[l.Address] = TcpServiceStats{
					ServiceStats: ServiceStats{
						Address:  l.Address,
						Protocol: "tcp",
					},
				}
			}

		}
		for _, c := range b.HttpConnectors {
			target := []ServiceTarget{
				ServiceTarget{
					Name:   iplookup.getPodName(c.Host),
					Target: getTargetName(c.Name),
					SiteId: c.SiteId,
				},
			}
			service, ok := httpServices[c.Address]
			if ok {
				service.Targets = append(service.Targets, target...)
				httpServices[c.Address] = service
			} else {
				service = HttpServiceStats{
					ServiceStats: ServiceStats{
						Address:  c.Address,
						Protocol: getHttpProtocol(c.ProtocolVersion),
						Targets:  target,
					},
				}
				httpServices[c.Address] = service
			}
		}
		for _, l := range b.HttpListeners {
			if _, ok := httpServices[l.Address]; !ok {
				httpServices[l.Address] = HttpServiceStats{
					ServiceStats: ServiceStats{
						Address:  l.Address,
						Protocol: getHttpProtocol(l.ProtocolVersion),
					},
				}
			}

		}
	}
	for i, c := range tcpconnections {
		tcpServices.updateTcpConnectionStats(sites[i].SiteId, c, iplookup)
	}

	services := []interface{}{}
	for _, s := range httpServices {
		services = append(services, s)
	}
	for _, s := range tcpServices {
		services = append(services, s)
	}
	return services
}


type ConnectionStatsIndex map[string]map[string]ConnectionStats

func asConnectionStats(connection *qdr.TcpConnection, iplookup *IpLookup) ConnectionStats {
	stats := ConnectionStats{
		Id:        connection.Name,
		StartTime: connection.Uptime,
		LastOut:   connection.LastOut,
		LastIn:    connection.LastIn,
		BytesIn:   connection.BytesIn,
		BytesOut:  connection.BytesOut,
	}
	if connection.Direction == "in" {
		stats.Client = iplookup.getPodName(connection.Host)

	} else {
		stats.Server = iplookup.getPodName(connection.Host)
	}
	return stats
}

func (index ConnectionStatsIndex) updateTcpConnectionStats(c qdr.TcpConnection, iplookup *IpLookup) {
	byId, ok := index[c.Address]
	if ok {
		byId[c.Name] = asConnectionStats(&c, iplookup)
	} else {
		index[c.Address] = map[string]ConnectionStats{
			c.Name: asConnectionStats(&c, iplookup),
		}
	}
}

func (services TcpServiceStatsMap) updateTcpConnectionStats(siteId string, connections []qdr.TcpConnection, iplookup *IpLookup) {
	log.Printf("Updating tcp connection stats for %s %d", siteId, len(connections))
	ingress := ConnectionStatsIndex{}
	egress := ConnectionStatsIndex{}
	for _, c := range connections {
		if c.Direction == "in" {
			ingress.updateTcpConnectionStats(c, iplookup)
		} else {
			egress.updateTcpConnectionStats(c, iplookup)
		}
	}
	for _, service := range services {
		service.ConnectionsIngress = append(service.ConnectionsIngress, SiteConnections{
			SiteId:      siteId,
			Connections: ingress[service.Address],
		})
		service.ConnectionsEgress = append(service.ConnectionsEgress, SiteConnections{
			SiteId:      siteId,
			Connections: egress[service.Address],
		})
		services[service.Address] = service
		log.Printf("Adding site connection stats for %s to %s (%d %d)", siteId, service.Address, len(service.ConnectionsIngress), len(service.ConnectionsEgress))
	}
}

type Site struct {
	SiteName  string   `json:"site_name"`
	SiteId    string   `json:"site_id"`
	Connected []string `json:"connected"`
	Namespace string   `json:"namespace"`
	Url       string   `json:"url"`
	Edge      bool     `json:"edge"`
}

func replace(in []string, lookup map[string]string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = lookup[s]
	}
	return out
}

func getSiteInfo(routers []qdr.Router) []Site {
	sites := []Site{}
	lookup := map[string]string{}
	for _, r := range routers {
		lookup[r.Id] = r.SiteId
	}
	for _, r := range routers {
		sites = append(sites, Site{
			SiteId:    r.SiteId,
			Connected: replace(r.ConnectedTo, lookup),
			Edge:      r.Edge,
		})
	}
	return sites
}

type ConsoleData struct {
	Sites    []Site        `json:"sites"`
	Services []interface{} `json:"services"`
}

func getConsoleData(agent *qdr.Agent, iplookup *IpLookup) (*ConsoleData, error) {
	routers, err := agent.GetAllRouters()
	if err != nil {
		return nil, fmt.Errorf("Error retrieving routers: %s", err)
	}
	bridges, err := agent.GetBridges(routers)
	if err != nil {
		return nil, fmt.Errorf("Error retrieving bridge configuration: %s", err)
	}
	tcpConns, err := agent.GetTcpConnections(routers)
	if err != nil {
		return nil, fmt.Errorf("Error retrieving tcp connection stats: %s", err)
	}
	log.Printf("Bridge data: %#v", bridges)
	data := ConsoleData{
		Sites:    getSiteInfo(routers),
	}
	data.Services = getServiceStats(bridges, data.Sites, tcpConns, iplookup)
	//query each site for remaining information
	err = getAllSiteInfo(agent, data.Sites)
	if err != nil {
		return nil, fmt.Errorf("Error with site queries: %s", err)
	}
	return &data, nil
}
