package exports

import (
	"fmt"
	"sync"
)

const Port9085 = 9085

type StoredEvent map[string]interface{}

const (
	EventTimeStamp = "eventtimestamp"
	EventType      = "eventtype"
	EventSource    = "eventsource"
	EventValues    = "eventvalues"
)

type StoredEventValues map[string]interface{}

var (
	Mu        sync.Mutex
	AllEvents []StoredEvent
)

func (event StoredEvent) ToCsv() (out string) {
	shortValue, ok := event[EventValues].(StoredEvent)
	valuesString := ""
	if ok {
		for key, value := range shortValue {
			valuesString += fmt.Sprintf(",%s,%s", key, value)
		}
	} else {
		valuesString = fmt.Sprintf("%v", event[EventValues])
	}
	return fmt.Sprintf("%s,%s,%s%s",
		event[EventTimeStamp],
		event[EventType],
		event[EventSource],
		valuesString)
}
