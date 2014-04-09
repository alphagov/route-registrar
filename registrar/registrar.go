package registrar

import (
	"os"
	"os/signal"
	"syscall"
	"fmt"
	"time"

	"github.com/cloudfoundry/gibson"
	"github.com/cloudfoundry/yagnats"

	"github.com/cloudfoundry-incubator/route-registrar/config"
	. "github.com/cloudfoundry-incubator/route-registrar/healthchecker"
)

type Registrar struct {
	Config config.Config
	SignalChannel chan os.Signal
	HealthChecker HealthChecker
	previousHealthStatus bool
}

func NewRegistrar(clientConfig config.Config) *Registrar {
	registrar := new(Registrar)
	registrar.Config = clientConfig
	registrar.SignalChannel = make(chan os.Signal, 1)
	registrar.previousHealthStatus = false
	return registrar
}

func(registrar *Registrar) AddHealthCheckHandler(handler HealthChecker){
	registrar.HealthChecker = handler
}

type callbackFunction func()

func logWithTimestamp(format string, args ...interface{}) {
	fmt.Printf("[%s] - ", time.Now().Local())
	if (nil == args) {
		fmt.Printf(format)
	} else {
		fmt.Printf(format, args...)
	}
}

func(registrar *Registrar) RegisterRoutes() {
	messageBus := yagnats.NewClient()
	connectionInfo := yagnats.ConnectionInfo{
		registrar.Config.MessageBusServer.Host,
		registrar.Config.MessageBusServer.User,
		registrar.Config.MessageBusServer.Password,
	}

	err := messageBus.Connect(&connectionInfo)

	if err != nil {
		logWithTimestamp("Error connecting to NATS: %v\n", err)
		panic("Failed to connect to NATS bus.")
	}

	logWithTimestamp("Connected to NATS server %s, as user %s\n", registrar.Config.MessageBusServer.Host, registrar.Config.MessageBusServer.User)

	client := gibson.NewCFRouterClient(registrar.Config.ExternalIp, messageBus)

	// set up periodic registration
	client.Greet()

	done := make(chan bool)
	registrar.registerSignalHandler(done, client)

	if(registrar.HealthChecker != nil) {
		callbackPeriodically(1 * time.Second,
			func() { registrar.updateRegistrationBasedOnHealthCheck(client) },
			done)
	} else {
		client.Register(registrar.Config.Port, registrar.Config.ExternalHost)

		select {
		case <- done:
			return
		}
	}
}

func callbackPeriodically(duration time.Duration, callback callbackFunction, done chan bool) {
	interval:= time.NewTicker(duration)
	for stop := false; !stop; {
		select {
		case <- interval.C:
			callback()
		case stop = <- done:
			return
		}
	}
}

func (registrar *Registrar) updateRegistrationBasedOnHealthCheck(client *gibson.CFRouterClient) {
	current := registrar.HealthChecker.Check()
	if( (!current) && registrar.previousHealthStatus ){
		logWithTimestamp("Health check status changed to unavailabile; unregistering the route\n")
		client.Unregister(registrar.Config.Port, registrar.Config.ExternalHost)
	} else if( current && (!registrar.previousHealthStatus) ) {
		logWithTimestamp("Health check status changed to availabile; registering the route\n")
		client.Register(registrar.Config.Port, registrar.Config.ExternalHost)
	}
	registrar.previousHealthStatus = current
}

func(registrar *Registrar) registerSignalHandler(done chan bool, client *gibson.CFRouterClient) {
	go func() {
		select {
		case <-registrar.SignalChannel:
			logWithTimestamp("Received SIGTERM or SIGINT; unregistering the route\n")
			client.Unregister(registrar.Config.Port, registrar.Config.ExternalHost)
			done <- true
		}
	}()

	signal.Notify(registrar.SignalChannel, syscall.SIGINT, syscall.SIGTERM)
}
