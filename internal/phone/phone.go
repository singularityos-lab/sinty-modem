package phone

import (
	"errors"
	"time"
)

type Stage string

const (
	StageStarting    Stage = "starting"
	StageModem       Stage = "modem"
	StageSIM         Stage = "sim"
	StageLocked      Stage = "locked"
	StageAttach      Stage = "attach"
	StageRegistering Stage = "registering"
	StageData        Stage = "data"
	StageIMS         Stage = "ims"
	StageReady       Stage = "ready"
	StageFailed      Stage = "failed"
	StageOff         Stage = "off"
)

type SIM struct {
	State      string
	PINRetries int
	PUKRetries int
	Operator   string
}

type Status struct {
	Stage        Stage
	Error        string
	Radio        bool
	SIM          SIM
	Registration string
	Tech         string
	OperatorName string
	OperatorCode string
	Signal       int
	Voice        bool
	SMS          bool
	Data         bool
	IMS          string
	Firmware     string
	Manufacturer string
	Model        string
	Emergency    []string
}

type CallState string

const (
	CallDialing    CallState = "dialing"
	CallRingingOut CallState = "ringing-out"
	CallRingingIn  CallState = "ringing-in"
	CallWaiting    CallState = "waiting"
	CallActive     CallState = "active"
	CallHeld       CallState = "held"
	CallTerminated CallState = "terminated"
)

type Call struct {
	ID         string
	Number     string
	Incoming   bool
	State      CallState
	Reason     string
	Since      time.Time
	Multiparty bool
}

type Message struct {
	ID        string
	Number    string
	Text      string
	Data      []byte
	Timestamp time.Time
	Incoming  bool
	State     string
	Class     int
}

type EventKind int

const (
	EventStatus EventKind = iota
	EventCall
	EventCallGone
	EventMessage
	EventMessageGone
)

type Event struct {
	Kind    EventKind
	Call    *Call
	Message *Message
}

var (
	ErrNotReady = errors.New("cellular service is not registered yet")
	ErrNoCall   = errors.New("this call is no longer available")
	ErrBusy     = errors.New("end the current call before placing another")
	ErrBadAPN   = errors.New("this is not a valid access point name")
	ErrWrongPUK = errors.New("wrong PUK")
	ErrWrongPIN = errors.New("wrong PIN")
)

type Network struct {
	Code   string
	Status uint32
}

type Backend interface {
	Status() Status
	Calls() []Call
	Messages() []Message
	Dial(number string) (Call, error)
	Accept(id string) error
	Hangup(id string) error
	DTMF(id string, keys string) error
	SetMute(on bool) error
	SendMessage(number, text string) (Message, error)
	DeleteMessage(id string) error
	SendPIN(pin string) (int, error)
	SendPUK(puk, pin string) (int, error)
	ChangePIN(old, next string) (int, error)
	SearchNetworks() ([]Network, error)
	SelectNetwork(code string) error
	SetRadio(on bool) error
	SelectSlot(slot int) error
	Restart() error
	Subscribe() (<-chan Event, func())
}
