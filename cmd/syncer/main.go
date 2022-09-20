package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/skupperproject/skupper/pkg/version"
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

func main() {
	// if -version used, report and exit
	isVersion := flag.Bool("version", false, "Report the version of the syncer")
	flag.Parse()
	if *isVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}


	// set up signals so we handle the first shutdown signal gracefully
	stopCh := SetupSignalHandler()

	syncer, err := NewSyncer()
	if err != nil {
		log.Fatal("Error getting new syncer ", err.Error())
	}

	if err = syncer.Run(stopCh); err != nil {
		log.Fatal("Error running syncer: ", err.Error())
	}
}
