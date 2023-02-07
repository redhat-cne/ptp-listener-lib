package exports

import (
	"fmt"
	"sync"
)

const Port9085 = 9085

type StoredEvent map[string]interface{}

const (
	EventTimeStamp   = "eventtimestamp"
	EventName        = "eventname"
	EventType        = "eventtype"
	EventSource      = "eventsource"
	EventValuesFull  = "eventvaluesfull"
	EventValuesShort = "eventvaluesshort"
)

type StoredEventValues map[string]interface{}

var (
	Mu        sync.Mutex
	AllEvents []StoredEvent
)

func (event StoredEvent) ToCsv() (out string) {
	return fmt.Sprintf("%s,%s,%s,%s,%s,%s", event[EventTimeStamp],
		event[EventName],
		event[EventType],
		event[EventSource],
		event[EventValuesFull],
		event[EventValuesShort])
}
