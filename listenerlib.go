package listenerlib

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"fmt"
	"io"
	"net/url"
	"time"

	"log"
	"net"

	"github.com/anthhub/forwarder"
	"github.com/redhat-cne/sdk-go/pkg/event"
	ptpEvent "github.com/redhat-cne/sdk-go/pkg/event/ptp"
	"github.com/redhat-cne/sdk-go/pkg/pubsub"
	"github.com/sirupsen/logrus"
	exports "github.com/test-network-function/ptp-listener-exports"

	"github.com/redhat-cne/sdk-go/pkg/types"
	api "github.com/redhat-cne/sdk-go/v1/pubsub"
	chanpubsub "github.com/test-network-function/channel-pubsub"
)

const (
	SubscriptionPath  = "/api/cloudNotifications/v1/subscriptions"
	LocalEventPath    = "/event"
	LocalHealthPath   = "/health"
	LocalAckEventPath = "/ack/event"
	HTTP200           = 200
)

var (
	Ps                           *chanpubsub.Pubsub
	CurrentSubscriptionIDPerType map[string]string
	localListeningEndpoint       string
)

func InitPubSub() {
	Ps = chanpubsub.NewPubsub()
}

func initResources(nodeName string) (resourceList []string) {
	CurrentSubscriptionIDPerType = make(map[string]string)
	// adding support for OsClockSyncState
	resourceList = append(resourceList, fmt.Sprintf(resourcePrefix, nodeName, string(ptpEvent.OsClockSyncState)))
	// add more events here
	return
}

var HTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 20,
	},
	Timeout: 10 * time.Second,
}

// Consumer webserver
func server(localListeningEndpoint string) {
	http.HandleFunc(LocalEventPath, getEvent)
	http.HandleFunc(LocalHealthPath, health)
	http.HandleFunc(LocalAckEventPath, ackEvent)
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
	if aTime == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
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
	supportedResources := initResources(nodeName)
	localListeningEndpoint = GetOutboundIP(kubernetesHost).String() + localListeningPort
	go server(localListeningPort) // spin local api
	time.Sleep(sleepSeconds * time.Second)
	for _, resource := range supportedResources {
		err := SubscribeAllEvents(resource, apiAddr, localListeningEndpoint)
		if err != nil {
			logrus.Errorf("could not register resource=%s at api addr=%s with endpoint=%s , err=%s", resource, apiAddr, localListeningEndpoint, err)
		}
	}
	logrus.Info("waiting for events")
}

func UnsubscribeAllEvents(kubernetesHost, nodeName, apiAddr string) {
	if localListeningEndpoint == "" {
		logrus.Error("local endpoint not available, cannot unsubscribe events")
		return
	}
	supportedResources := initResources(nodeName)
	time.Sleep(sleepSeconds * time.Second)
	for _, resource := range supportedResources {
		err := deleteSubscription(resource, apiAddr)
		if err != nil {
			logrus.Errorf("could not delete resource=%s at api addr=%s with endpoint=%s , err=%s", resource, apiAddr, localListeningEndpoint, err)
		}
	}
	logrus.Info("waiting for events")
}

func SubscribeAllEvents(supportedResource, apiAddr, localAPIAddr string) (err error) {
	subURL := &types.URI{URL: url.URL{Scheme: "http",
		Host: apiAddr,
		Path: SubscriptionPath}}
	endpointURL := &types.URI{URL: url.URL{Scheme: "http",
		Host: localAPIAddr,
		Path: "event"}}
	sub := api.NewPubSub(
		endpointURL,
		supportedResource)

	data, err := json.Marshal(&sub)
	if err != nil {
		return err
	}
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", subURL.String(), bytes.NewBuffer(data))
	if err != nil {
		logrus.Error(err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logrus.Error(err)
		return err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("error reading event %v", err)
	}
	logrus.Infof("%s", string(bodyBytes))
	respPubSub := pubsub.PubSub{}
	err = json.Unmarshal(bodyBytes, &respPubSub)
	if err != nil {
		logrus.Errorf("error un-marshaling event %v", err)
	}
	logrus.Infof("Subscription to %s created successfully with UUID=%s", supportedResource, respPubSub.ID)
	CurrentSubscriptionIDPerType[supportedResource] = respPubSub.ID
	return nil
}
func deleteSubscription(supportedResource, apiAddr string) (err error) {
	subURL := &types.URI{URL: url.URL{Scheme: "http",
		Host: apiAddr,
		Path: SubscriptionPath + "/" + CurrentSubscriptionIDPerType[supportedResource]}}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", subURL.String(), http.NoBody)
	if err != nil {
		logrus.Error(err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logrus.Error(err)
		return err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.Errorf("error reading event %v", err)
	}
	bodyString := string(bodyBytes)

	if resp.StatusCode != HTTP200 {
		return fmt.Errorf("failed deleting subscription ID=%s for type %s body=%s", CurrentSubscriptionIDPerType[supportedResource], supportedResource, bodyString)
	}

	logrus.Infof("subscription ID=%s for type %s deleted successfully", CurrentSubscriptionIDPerType[supportedResource], supportedResource)

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
