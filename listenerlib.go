package listernerlib

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"log"
	"net"

	"github.com/anthhub/forwarder"
	"github.com/redhat-cne/sdk-go/pkg/event"
	ptpEvent "github.com/redhat-cne/sdk-go/pkg/event/ptp"
	"github.com/sirupsen/logrus"
	exports "github.com/test-network-function/ptp-listerner-exports"

	"github.com/google/uuid"
	httpevents "github.com/redhat-cne/sdk-go/pkg/protocol/http"
	"github.com/redhat-cne/sdk-go/pkg/pubsub"
	"github.com/redhat-cne/sdk-go/pkg/subscriber"
	"github.com/redhat-cne/sdk-go/pkg/types"
	chanpubsub "github.com/test-network-function/channel-pubsub"
)

var (
	Ps *chanpubsub.Pubsub
)

func InitPubSub() {
	Ps = chanpubsub.NewPubsub()
}

func initSubscribers() map[string]string {
	subscribeTo := make(map[string]string)
	subscribeTo[string(ptpEvent.OsClockSyncStateChange)] = string(ptpEvent.OsClockSyncState)
	return subscribeTo
}

// Consumer webserver
func server(localListeningEndpoint string) {
	http.HandleFunc("/event", getEvent)
	http.HandleFunc("/health", health)
	http.HandleFunc("/ack/event", ackEvent)
	server := &http.Server{
		Addr:              localListeningEndpoint,
		ReadHeaderTimeout: 3 * time.Second,
	}
	err := server.ListenAndServe()
	if err != nil {
		logrus.Errorf("ListenAndServe returns with err= %s", err)
	}
}

func health(w http.ResponseWriter, req *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// net/http.Header ["User-Agent": ["Go-http-client/1.1"],
// "Ce-Subject": ["/cluster/node/master2/sync/ptp-status/ptp-clock-class-change"],
// "Content-Type": ["application/json"],
// "Accept-Encoding": ["gzip"],
// "Content-Length": ["138"],
// "Ce-Id": ["4eff05f8-493a-4382-8d89-209dc2179041"],
// "Ce-Source": ["/cluster/node/master2/sync/ptp-status/ptp-clock-class-change"],
// "Ce-Specversion": ["0.3"], "Ce-Time": ["2022-12-16T14:26:47.167232673Z"],
// "Ce-Type": ["event.sync.ptp-status.ptp-clock-class-change"], ]

func getEvent(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	aSource := req.Header.Get("Ce-Source")
	aType := req.Header.Get("Ce-Type")
	aTime, err := types.ParseTimestamp(req.Header.Get("Ce-Time"))
	if err != nil {
		logrus.Error(err)
	}
	logrus.Debug(aTime.String())
	logrus.Debug(aSource)

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		logrus.Errorf("error reading event %v", err)
	}
	e := string(bodyBytes)

	if e != "" {
		logrus.Debugf("received event %s", string(bodyBytes))

		aEvent, err := createStoredEvent(aSource, aType, aTime.Time, bodyBytes)
		if err != nil {
			logrus.Errorf("could not create event %s", err)
		}
		logrus.Info(aEvent)
		Ps.Publish(aType, aEvent)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func ackEvent(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		logrus.Errorf("error reading acknowledgment  %v", err)
	}
	e := string(bodyBytes)
	if e != "" {
		logrus.Infof("received ack %s", string(bodyBytes))
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

const (
	resourcePrefix     string = "/cluster/node/%s%s"
	localListeningPort string = ":8989"
	sleepSeconds              = 5
)

func RegisterAnWaitForEvents(kubernetesHost, nodeName, apiAddr string) {
	subscribeTo := initSubscribers()
	var wg sync.WaitGroup
	wg.Add(1)
	localListeningEndpoint := GetOutboundIP(kubernetesHost).String() + localListeningPort
	go server(localListeningPort) // spin local api
	time.Sleep(sleepSeconds * time.Second)

	var subs []pubsub.PubSub
	for _, resource := range subscribeTo {
		subs = append(subs, pubsub.PubSub{
			ID:       uuid.New().String(),
			Resource: fmt.Sprintf(resourcePrefix, nodeName, resource),
		})
	}

	// if AMQ enabled the subscription will create an AMQ listener client
	// IF HTTP enabled, the subscription will post a subscription  requested to all
	// publishers that are defined in http-event-publisher variable

	e := createSubscription(subs, apiAddr, localListeningEndpoint)
	if e != nil {
		logrus.Error(e)
		return
	}
	logrus.Info("waiting for events")
}

func createSubscription(subscriptions []pubsub.PubSub, apiAddr, localAPIAddr string) (err error) {
	subURL := &types.URI{URL: url.URL{Scheme: "http",
		Host: apiAddr,
		Path: "subscription"}}
	endpointURL := &types.URI{URL: url.URL{Scheme: "http",
		Host: localAPIAddr,
		Path: ""}}

	subs := subscriber.New(uuid.UUID{})
	// Self URL
	_ = subs.SetEndPointURI(endpointURL.String())

	// create a subscriber model
	subs.AddSubscription(subscriptions...)

	ce, _ := subs.CreateCloudEvents()
	ce.SetSubject("1")
	ce.SetSource(subscriptions[0].Resource)
	ce.SetDataContentType("application/json")

	logrus.Debug(ce)

	if err := httpevents.Post(subURL.String(), *ce); err != nil {
		return fmt.Errorf("(1)error creating: %v at  %s with data %s=%s", err, subURL.String(), ce.String(), ce.Data())
	}

	return nil
}

// Get preferred outbound ip of this machine
func GetOutboundIP(aURL string) net.IP {
	u, err := url.Parse(aURL)
	if err != nil {
		log.Fatalf("cannot parse k8s api url, would not receive events, stopping, err = %s", err)
	}

	conn, err := net.Dial("udp", u.Host)
	if err != nil {
		log.Fatalf("error dialing host or address (%s), err = %s", u.Host, err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	logrus.Infof("Outbound IP = %s to reach server: %s", localAddr.IP.String(), u.Host)
	return localAddr.IP
}

func StartListening(ptpEventServiceLocalhostPort,
	ptpEventServiceRemotePort int,
	ptpPodName,
	nodeName,
	ptpNs,
	kubeconfigPath,
	kubernetesHost string,
) error {
	options := []*forwarder.Option{
		{
			LocalPort:  ptpEventServiceLocalhostPort,
			RemotePort: ptpEventServiceRemotePort,
			Namespace:  ptpNs,
			PodName:    ptpPodName,
		},
	}
	logrus.Infof("Forwarding to pod: %s node: %s", ptpPodName, nodeName)

	ret, err := forwarder.WithForwarders(context.Background(), options, kubeconfigPath)
	if err != nil {
		logrus.Error("WithForwarders err: ", err)
		os.Exit(1)
	}
	// remember to close the forwarding
	defer ret.Close()
	// wait forwarding ready
	// the remote and local ports are listed
	ports, err := ret.Ready()
	if err != nil {
		ret.Close()
		return fmt.Errorf("could not initialize port forwarding, err=%s", err)
	}
	fmt.Printf("ports: %+v\n", ports)
	RegisterAnWaitForEvents(kubernetesHost, nodeName, "localhost:"+strconv.Itoa(ptpEventServiceLocalhostPort))
	return nil
}

func createStoredEvent(source, eventType string, eventTime time.Time, data []byte) (aStoredEvent exports.StoredEvent, err error) {
	var e event.Data
	err = json.Unmarshal(data, &e)
	if err != nil {
		return aStoredEvent, err
	}
	logrus.Debug(e)

	// Note that there is no UnixMillis, so to get the
	// milliseconds since epoch you'll need to manually
	// divide from nanoseconds.
	latency := (time.Now().UnixNano() - eventTime.UnixNano()) / 1000000
	// set log to Info level for performance measurement
	logrus.Debugf("Latency for the event: %d ms\n", latency)

	valuesFull := exports.StoredEventValues{}
	valuesShort := exports.StoredEventValues{}
	for _, v := range e.Values {
		dataType := string(v.DataType)
		valueType := string(v.ValueType)
		resource := v.Resource
		valuesFull[resource+"_"+dataType+"_"+valueType] = v.Value
		valuesShort[dataType] = v.Value
	}
	return exports.StoredEvent{exports.EventTimeStamp: eventTime, exports.EventType: eventType, exports.EventSource: source,
		exports.EventValuesFull: valuesFull, exports.EventValuesShort: valuesShort}, nil
}
