package exports

import (
	"sync"
)

const Port9043 = 9043

type StoredEvent map[string]interface{}

const (
	EventTimeStamp   = "eventtimestamp"
	EventName        = "eventname"
	EventType        = "eventtype"
	EventSource      = "eventsource"
	EventValuesFull  = "eventvaluesfull"
	EventValuesShort = "eventvaluesshort"
	SessionUID       = "sessionuid"
)

type StoredEventValues map[string]interface{}

var (
	Mu        sync.Mutex
	AllEvents []StoredEvent
)
