package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
	"github.com/skupperproject/skupper/pkg/qdr"
)

func describe(i interface{}) {
	fmt.Printf("(%v, %T)\n", i, i)
	fmt.Println()
}

var onlyOneSignalHandler = make(chan struct{})
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func SetupSignalHandler() (stopCh <-chan struct{}) {
	close(onlyOneSignalHandler) // panics when called twice

	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		<-c
		close(stop)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()

	return stop
}

const NamespaceFile string = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

func main() {
	// if -version used, report and exit
	isVersion := flag.Bool("version", false, "Report the version of the Skupper Service Controller")
	flag.Parse()
	if *isVersion {
		fmt.Println(client.Version)
		os.Exit(0)
	}

	// Startup message
	log.Printf("Skupper service controller")
	log.Printf("Version: %s", client.Version)

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := SetupSignalHandler()

	namespace := ""
	_, err := os.Stat(NamespaceFile)
	if err == nil {
		raw, err := ioutil.ReadFile(NamespaceFile)
		if err != nil {
			log.Println("Error reading namespace", err.Error())
		}
		namespace = string(raw)
	} else if !os.IsNotExist(err) {
		log.Println("Error checking for namespace file", err.Error())
	}

	log.Println("Initialising kubernetes client")
	// todo, get context from env?
	cli, err := client.NewClient(namespace, "", "")
	if err != nil {
		log.Fatal("Error getting van client", err.Error())
	}

	event.StartDefaultEventStore(stopCh)

	//create initial resources
	siteMgr := kube.NewSiteManager(cli.Namespace, cli.KubeClient, cli.RouteClient)
	factory := &qdr.ConnectionFactory{}
	consoleServer := newConsoleServer(cli, factory, siteMgr)
	localAddr := "localhost:8181"
	go func() {
		err = consoleServer.local.ListenAndServe(localAddr, nil)
		log.Println(err.Error())
	}()
	log.Printf("Local http server listening on %s", localAddr)

	siteMgr.SetConsoleControl(consoleServer.public)
	err = siteMgr.Start(stopCh)
	if err != nil {
		log.Fatal("Error initialising site:", err.Error())
	}
	amqpClientCredentials, err := siteMgr.GetAmqpClientCredentials()
	if err != nil {
		log.Fatal("Error getting tls config for amqp client", err.Error())
	}
	factory.Configure("amqps://"+types.LocalTransportServiceName+":5671", amqpClientCredentials)

	siteConfig := siteMgr.GetSiteConfig()
	origin := siteMgr.GetSiteId()

	controller, err := NewController(cli, origin, factory, !siteConfig.EnableServiceSync)
	if err != nil {
		log.Fatal("Error getting new controller", err.Error())
	}

	controller.siteQueryServer = newSiteQueryServer(cli, factory)

	log.Println("Waiting for Skupper router component to start")
	_, err = kube.WaitDeploymentReady(types.TransportDeploymentName, cli.Namespace, cli.KubeClient, time.Second*180, time.Second*5)
	if err != nil {
		log.Fatal("Error waiting for transport deployment to be ready: ", err.Error())
	}

	// start the controller workers
	if err = controller.Run(stopCh); err != nil {
		log.Fatal("Error running controller: ", err.Error())
	}
}
