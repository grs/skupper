package data

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gorilla/mux"

	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/types"
)

const (
	HttpInternalServerError string = "HttpServerError"
	HttpAuthFailure         string = "HttpAuthenticationFailure"
	SiteVersionConflict     string = "SiteVersionConflict"
)

type ConsoleServer struct {
	agentPool *qdr.AgentPool
	tokens    TokenManager
	links     LinkManager
	services  ServiceManager
	config    *tls.Config
	address   string
	auth      types.ConsoleAuth
	public    *types.HttpServer
	local     *types.HttpServer
}

func NewConsoleServer(factory *qdr.ConnectionFactory, auth types.ConsoleAuth, tokenMgr TokenManager, linkMgr LinkManager, svcMgr ServiceManager) *ConsoleServer {
	pool := qdr.NewAgentPool(factory)
	server := &ConsoleServer{
		agentPool: pool,
		tokens:    tokenMgr,
		links:     linkMgr,
		services:  svcMgr,
		auth:      auth,
	}
	server.public = types.NewHttpServer(server.publicHandler())
	server.local = types.NewHttpServer(server.localHandler())
	return server
}

func  (server *ConsoleServer) authenticated(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if server.auth.RequireAuthentication() {
			user, password, _ := r.BasicAuth()

			if server.auth.Authenticate(user, password) {
				h.ServeHTTP(w, r)
			} else {
				w.Header().Set("WWW-Authenticate", "Basic realm=skupper")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			}
		} else {
			h.ServeHTTP(w, r)
		}
	})
}

type VersionInfo struct {
	ServiceControllerVersion string `json:"service_controller_version"`
	RouterVersion            string `json:"router_version"`
	SiteVersion              string `json:"site_version"`
}

func (server *ConsoleServer) httpInternalError(w http.ResponseWriter, err error) {
	event.Record(HttpInternalServerError, err.Error())
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (server *ConsoleServer) version() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := VersionInfo{
			ServiceControllerVersion: types.Version,
		}
		agent, err := server.agentPool.Get()
		if err != nil {
			server.httpInternalError(w, fmt.Errorf("Could not get management agent : %s", err))
			return
		}
		router, err := agent.GetLocalRouter()
		server.agentPool.Put(agent)
		if err != nil {
			server.httpInternalError(w, fmt.Errorf("Error retrieving local router version: %s", err))
			return
		}
		v.RouterVersion = router.Version
		v.SiteVersion = router.Site.Version
		if wantsJsonOutput(r) {
			bytes, err := json.MarshalIndent(v, "", "    ")
			if err != nil {
				server.httpInternalError(w, fmt.Errorf("Error writing version: %s", err))
				return
			}
			fmt.Fprintf(w, string(bytes)+"\n")
		} else {
			tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
			fmt.Fprintln(tw, "site\t"+v.SiteVersion)
			fmt.Fprintln(tw, "service-controller\t"+v.ServiceControllerVersion)
			fmt.Fprintln(tw, "router\t"+v.RouterVersion)
			tw.Flush()
		}
	})
}

func (server *ConsoleServer) site() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, os.Getenv("SKUPPER_SITE_ID"))
	})
}

const (
	MaxFieldLength int = 60
)

func wrap(text string, width int) []string {
	words := strings.Fields(text)
	wrapped := []string{}
	line := ""
	for _, word := range words {
		if len(word)+len(line)+1 > width {
			wrapped = append(wrapped, line)
			line = word
		} else {
			if line == "" {
				line = word
			} else {
				line = line + " " + word
			}
		}
	}
	wrapped = append(wrapped, line)
	return wrapped
}

func wantsJsonOutput(r *http.Request) bool {
	options := r.URL.Query()
	output := options.Get("output")
	return output == "json"
}

func (server *ConsoleServer) serveEvents() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := event.Query()
		if wantsJsonOutput(r) {
			bytes, err := json.MarshalIndent(e, "", "    ")
			if err != nil {
				server.httpInternalError(w, fmt.Errorf("Error writing events: %s", err))
				return
			}
			fmt.Fprintf(w, string(bytes)+"\n")
		} else {
			tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
			fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s", "NAME", "COUNT", " ", "AGE"))
			for _, group := range e {
				fmt.Fprintln(tw, fmt.Sprintf("%s\t%d\t%s\t%s", group.Name, group.Total, " ", time.Since(group.LastOccurrence).Round(time.Second)))
				for _, detail := range group.Counts {
					if len(detail.Key) > MaxFieldLength {
						lines := wrap(detail.Key, MaxFieldLength)
						for i, line := range lines {
							if i == 0 {
								fmt.Fprintln(tw, fmt.Sprintf("%s\t%d\t%s\t%s", " ", detail.Count, line, time.Since(detail.LastOccurrence).Round(time.Second)))
							} else {
								fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s", " ", " ", line, ""))
							}
						}
					} else {
						fmt.Fprintln(tw, fmt.Sprintf("%s\t%d\t%s\t%s", " ", detail.Count, detail.Key, time.Since(detail.LastOccurrence).Round(time.Second)))
					}
				}
			}
			tw.Flush()
		}
	})
}

func (server *ConsoleServer) serveSites() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := server.getData(w)
		if d != nil {
			if wantsJsonOutput(r) {
				bytes, err := json.MarshalIndent(d.Sites, "", "    ")
				if err != nil {
					server.httpInternalError(w, fmt.Errorf("Error writing json: %s", err))
				} else {
					fmt.Fprintf(w, string(bytes)+"\n")
				}
			} else {
				tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
				fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s", "ID", "NAME", "EDGE", "VERSION", "NAMESPACE", "URL", "CONNECTED TO"))
				for _, site := range d.Sites {
					fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%t\t%s\t%s\t%s\t%s", site.SiteId, site.SiteName, site.Edge, site.Version, site.Namespace, site.Url, strings.Join(site.Connected, " ")))
				}
				tw.Flush()

			}
		}
	})
}

func (server *ConsoleServer) serveServices() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := server.getData(w)
		if d != nil {
			if wantsJsonOutput(r) {
				bytes, err := json.MarshalIndent(d.Services, "", "    ")
				if err != nil {
					server.httpInternalError(w, fmt.Errorf("Error writing json: %s", err))
				} else {
					fmt.Fprintf(w, string(bytes)+"\n")
				}
			} else {
				tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
				fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s", "ADDRESS", "PROTOCOL", "TARGET", "SITE"))
				for _, s := range d.Services {
					var service *Service
					if hs, ok := s.(HttpService); ok {
						service = &hs.Service
					}
					if ts, ok := s.(TcpService); ok {
						service = &ts.Service
					}
					if service != nil {
						fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s", service.Address, service.Protocol, "", ""))
						for _, target := range service.Targets {
							fmt.Fprintln(tw, fmt.Sprintf("%s\t%s\t%s\t%s", "", "", target.Name, target.SiteId))
						}
					}
				}
				tw.Flush()
			}
		}
	})
}

func removeEmpty(input []string) []string {
	output := []string{}
	for _, s := range input {
		if s != "" {
			output = append(output, s)
		}
	}
	return output
}

func (server *ConsoleServer) checkService() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent, err := server.agentPool.Get()
		if err != nil {
			server.httpInternalError(w, fmt.Errorf("Could not get management agent : %s", err))
		} else {
			//what is the name of the service to check?
			vars := mux.Vars(r)
			if address, ok := vars["name"]; ok {
				data, err := checkService(agent, address)
				server.agentPool.Put(agent)
				if err != nil {
					server.httpInternalError(w, err)
				} else {
					if wantsJsonOutput(r) {
						bytes, err := json.MarshalIndent(data, "", "    ")
						if err != nil {
							server.httpInternalError(w, fmt.Errorf("Error writing json: %s", err))
						} else {
							fmt.Fprintf(w, string(bytes)+"\n")
						}
					} else {
						if len(data.Observations) > 0 {
							for _, observation := range data.Observations {
								fmt.Fprintln(w, observation)
							}
							if data.HasDetailObservations() {
								fmt.Fprintln(w, "")
								fmt.Fprintln(w, "Details:")
								fmt.Fprintln(w, "")
								tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
								for _, site := range data.Details {
									for i, observation := range site.Observations {
										if i == 0 {
											fmt.Fprintln(tw, fmt.Sprintf("%s\t%s", site.SiteId, observation))
										} else {
											fmt.Fprintln(tw, fmt.Sprintf("%s\t%s", "", observation))
										}
									}
								}
								tw.Flush()
							}
						} else {
							fmt.Fprintln(w, "No issues found")
						}
					}
				}
			} else {
				http.Error(w, "Invalid path", http.StatusNotFound)
			}
		}
	})
}

func (server *ConsoleServer) getData(w http.ResponseWriter) *ConsoleData {
	agent, err := server.agentPool.Get()
	if err != nil {
		server.httpInternalError(w, fmt.Errorf("Could not get management agent : %s", err))
		return nil
	}
	data, err := getConsoleData(agent)
	server.agentPool.Put(agent)
	if err != nil {
		server.httpInternalError(w, err)
		return nil
	}
	return data
}

func (server *ConsoleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data := server.getData(w)
	if data != nil {
		bytes, err := json.MarshalIndent(data, "", "    ")
		if err != nil {
			server.httpInternalError(w, fmt.Errorf("Error writing json: %s", err))
		} else {
			fmt.Fprintf(w, string(bytes)+"\n")
		}
	}
}

func writeJson(obj interface{}, w http.ResponseWriter) {
	bytes, err := json.MarshalIndent(obj, "", "    ")
	if err != nil {
		event.Record(HttpInternalServerError, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, string(bytes)+"\n")
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE")
		next.ServeHTTP(w, r)
	})
}

func (server *ConsoleServer) publicHandler() http.Handler {
	r := mux.NewRouter()
	r.Handle("/DATA", server.authenticated(server))
	r.Handle("/tokens", server.authenticated(serveTokens(server.tokens)))
	r.Handle("/tokens/", server.authenticated(serveTokens(server.tokens)))
	r.Handle("/tokens/{name}", server.authenticated(serveTokens(server.tokens)))
	r.Handle("/downloadclaim/{name}", server.authenticated(downloadClaim(server.tokens)))
	r.Handle("/links", server.authenticated(serveLinks(server.links)))
	r.Handle("/links/", server.authenticated(serveLinks(server.links)))
	r.Handle("/links/{name}", server.authenticated(serveLinks(server.links)))
	r.Handle("/services", server.authenticated(serveServices(server.services)))
	r.Handle("/services/", server.authenticated(serveServices(server.services)))
	r.Handle("/services/{name}", server.authenticated(serveServices(server.services)))
	r.Handle("/targets", server.authenticated(serveTargets(server.services)))
	r.Handle("/targets/", server.authenticated(serveTargets(server.services)))
	r.Handle("/version", server.authenticated(server.version()))
	r.Handle("/site", server.authenticated(server.site()))
	r.Handle("/events", server.authenticated(server.serveEvents()))
	r.Handle("/servicecheck/{name}", server.authenticated(server.checkService()))
	r.PathPrefix("/").Handler(server.authenticated(http.FileServer(http.Dir("/app/console/"))))
	if os.Getenv("USE_CORS") != "" {
		r.Use(cors)
	}
	return r
}

func (server *ConsoleServer) localHandler() http.Handler {
	r := mux.NewRouter()
	r.Handle("/DATA", server)
	r.Handle("/version", server.version())
	r.Handle("/events", server.serveEvents())
	r.Handle("/sites", server.serveSites())
	r.Handle("/services", server.serveServices())
	r.Handle("/servicecheck/{name}", server.checkService())
	return r
}

func set(m map[string]map[string]bool, k1 string, k2 string) {
	m2, ok := m[k1]
	if !ok {
		m2 = map[string]bool{}
	}
	m2[k2] = true
	m[k1] = m2
}

func getAllSites(routers []qdr.Router) []SiteQueryData {
	sites := map[string]SiteQueryData{}
	routerToSite := map[string]string{}
	siteConnections := map[string]map[string]bool{}
	for _, r := range routers {
		routerToSite[r.Id] = r.Site.Id
		site, exists := sites[r.Site.Id]
		if !exists {
			if !r.IsGateway() {
				sites[r.Site.Id] = SiteQueryData{
					Site: Site{
						SiteId:    r.Site.Id,
						Version:   r.Site.Version,
						Edge:      r.Edge && strings.Contains(r.Id, "skupper-router"),
						Connected: []string{},
						Gateway:   false,
					},
				}
			}
		} else if r.Site.Version != site.Version {
			event.Recordf(SiteVersionConflict, "Conflicting site version for %s: %s != %s", site.SiteId, site.Version, r.Site.Version)
		}
	}
	for _, r := range routers {
		for _, id := range r.ConnectedTo {
			set(siteConnections, r.Site.Id, routerToSite[id])
		}
	}
	list := []SiteQueryData{}
	for _, s := range sites {
		m := siteConnections[s.SiteId]
		for key, _ := range m {
			s.Connected = append(s.Connected, key)
		}
		list = append(list, s)
	}
	return list
}

func getConsoleData(agent *qdr.Agent) (*ConsoleData, error) {
	routers, err := agent.GetAllRouters()
	if err != nil {
		return nil, fmt.Errorf("Error retrieving routers: %s", err)
	}
	sites := getAllSites(routers)
	querySites(agent, sites)
	for i, s := range sites {
		if s.Version == "" {
			// prior to 0.5 there was no version in router metadata
			// and site query did not return services, so they are
			// retrieved here separately
			err = getServiceInfo(agent, routers, &sites[i], NewNullNameMapping())
			if err != nil {
				return nil, fmt.Errorf("Error retrieving service data from old site %s: %s", s.SiteId, err)
			}
		}
	}
	gateways := queryGateways(agent, sites)
	sites = append(sites, gateways...)
	consoleData := &ConsoleData{}
	consoleData.Merge(sites)
	return consoleData, nil
}

func checkService(agent *qdr.Agent, address string) (*ServiceCheck, error) {
	//get all routers of version 0.5 and up
	routers, err := agent.GetAllRouters()
	if err != nil {
		return nil, fmt.Errorf("Error retrieving routers: %s", err)
	}
	allSites := getAllSites(routers)
	serviceCheck := ServiceCheck{}
	sites := map[string]Site{}
	for _, site := range allSites {
		if site.Version != "" {
			sites[site.SiteId] = site.Site
			serviceCheck.Details = append(serviceCheck.Details, ServiceDetail{
				SiteId: site.SiteId,
			})
		}
	}
	err = checkServiceForSites(agent, address, &serviceCheck)
	if err != nil {
		return nil, fmt.Errorf("Error retrieving service detail: %s", err)
	}
	return &serviceCheck, nil
}

type TokenState struct {
	Name            string     `json:"name"`
	ClaimsMade      *int       `json:"claimsMade"`
	ClaimsRemaining *int       `json:"claimsRemaining"`
	ClaimExpiration *time.Time `json:"claimExpiration"`
	Created         string     `json:"created,omitempty"`
}

type TokenManager interface {
	GetTokens() ([]TokenState, error)
	GetToken(name string) (*TokenState, error)
	DeleteToken(name string) (bool, error)
	DownloadClaim(name string) (interface{}, error)
	GenerateToken(options *TokenOptions) (interface{}, error)
}

type TokenOptions struct {
	Expiry time.Duration
	Uses   int
}

func (o *TokenOptions) setExpiration(value string) error {
	if value == "" {
		o.Expiry = 15 * time.Minute
		return nil
	}
	result, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return err
	}
	o.Expiry = result.Sub(time.Now())
	return nil
}

func (o *TokenOptions) setUses(value string) error {
	if value == "" {
		o.Uses = 1
		return nil
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	o.Uses = result
	return nil
}

func getTokenOptions(r *http.Request) (*TokenOptions, error) {
	options := &TokenOptions{}
	params := r.URL.Query()
	err := options.setExpiration(params.Get("expiration"))
	if err != nil {
		return nil, err
	}
	err = options.setUses(params.Get("uses"))
	if err != nil {
		return nil, err
	}
	return options, nil
}

func serveTokens(m TokenManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		if r.Method == http.MethodGet {
			if name, ok := vars["name"]; ok {
				token, err := m.GetToken(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if token == nil {
					http.Error(w, "No such token", http.StatusNotFound)
				} else {
					writeJson(token, w)
				}

			} else {
				tokens, err := m.GetTokens()
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else {
					writeJson(tokens, w)
				}
			}
		} else if r.Method == http.MethodPost {
			options, err := getTokenOptions(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				token, err := m.GenerateToken(options)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else {
					writeJson(token, w)
				}
			}
		} else if r.Method == http.MethodDelete {
			if name, ok := vars["name"]; ok {
				ok, err := m.DeleteToken(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if !ok {
					http.Error(w, "No such token", http.StatusNotFound)
				} else {
					event.Recordf("Token %s deleted", name)
				}
			} else {
				http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			}
		} else if r.Method != http.MethodOptions {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	})
}

func downloadClaim(m TokenManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		if r.Method == http.MethodGet {
			if name, ok := vars["name"]; ok {
				token, err := m.DownloadClaim(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if token == nil {
					http.Error(w, "No such token", http.StatusNotFound)
				} else {
					writeJson(token, w)
				}

			} else {
				http.Error(w, "Token must be specified in path", http.StatusNotFound)
			}
		} else {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	})
}


func getCost(r *http.Request) (int, error) {
	params := r.URL.Query()
	value := params.Get("cost")
	if value == "" {
		return 0, nil
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return result, nil
}

type LinkManager interface {
	GetLinks() ([]types.LinkStatus, error)
	GetLink(name string) (*types.LinkStatus, error)
	DeleteLink(name string) (bool, error)
	CreateLink(cost int, token []byte) error
}

func serveLinks(m LinkManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		if r.Method == http.MethodGet {
			if name, ok := vars["name"]; ok {
				link, err := m.GetLink(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if link == nil {
					http.Error(w, "No such link", http.StatusNotFound)
				} else {
					writeJson(link, w)
				}

			} else {
				links, err := m.GetLinks()
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else {
					writeJson(links, w)
				}
			}
		} else if r.Method == http.MethodPost {
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			cost, err := getCost(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			err = m.CreateLink(cost, body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		} else if r.Method == http.MethodDelete {
			if name, ok := vars["name"]; ok {
				ok, err := m.DeleteLink(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if !ok {
					http.Error(w, "No such link", http.StatusNotFound)
				} else {
					event.Recordf("Link %s deleted", name)
				}
			} else {
				http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			}
		} else if r.Method != http.MethodOptions {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	})
}


func (o *ServiceOptions) GetTargetName() string {
	return o.Target.Name
}

func (o *ServiceOptions) GetTargetType() string {
	return o.Target.Type
}

func (o *ServiceOptions) GetServiceName() string {
	if o.Address != "" {
		return o.Address
	}
	return o.Target.Name
}

func (o *ServiceOptions) GetProtocol() string {
	if o.Protocol != "" {
		return o.Protocol
	}
	return "tcp"
}

func (o *ServiceOptions) GetPorts() []int {
	if len(o.Ports) > 0 {
		return o.Ports
	}
	tPorts := []int{}
	for _, tPort := range o.TargetPorts {
		tPorts = append(tPorts, tPort)
	}
	return tPorts
}

func (o *ServiceOptions) GetTargetPorts() map[int]int {
	if len(o.Ports) == 0 {
		// in this case the port will have been set to the
		// target port, which does not then need overridden
		return map[int]int{}
	}
	return o.TargetPorts
}

func (o *ServiceOptions) DeducePort() bool {
	return len(o.Ports) == 0 && len(o.TargetPorts) == 0
}


type PortDescription struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type ServiceTargetDefinition struct {
	Name  string            `json:"name"`
	Type  string            `json:"type"`
	Ports []PortDescription `json:"ports,omitempty"`
}

type ServiceEndpoint struct {
	Name   string      `json:"name"`
	Target string      `json:"target"`
	Ports  map[int]int `json:"ports,omitempty"`
}

type ServiceDefinition struct {
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Ports     []int             `json:"ports"`
	Endpoints []ServiceEndpoint `json:"endpoints"`
}

type ServiceOptions struct {
	Address     string                  `json:"address"`
	Protocol    string                  `json:"protocol"`
	Ports       []int                   `json:"ports"`
	TargetPorts map[int]int             `json:"targetPorts,omitempty"`
	Labels      map[string]string       `json:"labels,omitempty"`
	Target      ServiceTargetDefinition `json:"target"`
}

type ServiceManager interface {
	GetServices() ([]ServiceDefinition, error)
	GetService(name string) (*ServiceDefinition, error)
	GetServiceTargets() ([]ServiceTargetDefinition, error)
	CreateService(options *ServiceOptions) error
	DeleteService(name string) (bool, error)
}

func getServiceOptions(r *http.Request) (*ServiceOptions, error) {
	options := &ServiceOptions{}
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return options, err
	}
	if len(body) == 0 {
		return options, fmt.Errorf("Target must be specified in request body")
	}
	err = json.Unmarshal(body, options)
	if err != nil {
		return options, err
	}
	if options.Target.Name == "" || options.Target.Type == "" {
		return options, fmt.Errorf("Target must be specified in request body")
	}
	return options, nil
}

func serveServices(m ServiceManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		if r.Method == http.MethodGet {
			if name, ok := vars["name"]; ok {
				service, err := m.GetService(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if service == nil {
					http.Error(w, "No such service", http.StatusNotFound)
				} else {
					writeJson(service, w)
				}

			} else {
				services, err := m.GetServices()
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else {
					writeJson(services, w)
				}
			}
		} else if r.Method == http.MethodPost {
			options, err := getServiceOptions(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				err := m.CreateService(options)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else {
					event.Recordf("Service %s exposed", options.GetServiceName())
				}
			}
		} else if r.Method == http.MethodDelete {
			if name, ok := vars["name"]; ok {
				deleted, err := m.DeleteService(name)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				} else if !deleted {
					http.Error(w, "No such service", http.StatusNotFound)
				} else {
					event.Recordf("Service %s deleted", name)
				}
			} else {
				http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			}
		} else if r.Method != http.MethodOptions {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	})
}

func serveTargets(m ServiceManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			targets, err := m.GetServiceTargets()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			} else {
				writeJson(targets, w)
			}
		} else if r.Method != http.MethodOptions {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	})
}
